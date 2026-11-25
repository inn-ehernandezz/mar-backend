"""Turn a local document into bounded fragments with source references."""

from functools import lru_cache
import csv
import os
from zipfile import ZipFile

FORMATS = {
    "application/pdf": ".pdf",
    "application/vnd.openxmlformats-officedocument.wordprocessingml.document": ".docx",
    "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": ".xlsx",
    "application/vnd.openxmlformats-officedocument.presentationml.presentation": ".pptx",
    "text/plain": ".txt",
    "text/markdown": ".md",
    "text/csv": ".csv",
}
MAX_FRAGMENTS = 3000
MAX_CHARS = 1000
MAX_PAGES = 200
MAX_CELLS = 100_000


class Rejected(ValueError):
    """The original cannot be processed as a supported document."""


def split_text(text):
    start, end = 0, len(text)
    while start < end:
        while start < end and text[start].isspace():
            start += 1
        if start == end:
            return
        cut = min(start + MAX_CHARS, end)
        if cut < end:
            space = text.rfind(" ", start + MAX_CHARS // 2, cut + 1)
            if space != -1:
                cut = space
        yield text[start:cut].rstrip()
        start = cut


def fragments(blocks):
    result = []
    for text, location in blocks:
        for fragment in split_text(text):
            result.append({"text": fragment, **location})
            if len(result) > MAX_FRAGMENTS:
                raise Rejected("too_many_fragments")
    if not result:
        raise Rejected("no_extractable_text")
    return result


def plain_blocks(path):
    section = ""
    with path.open(encoding="utf-8-sig") as stream:
        for line in stream:
            line = line.strip()
            if path.suffix == ".md" and line.startswith("# "):
                section = line[2:202]
            if line:
                yield line.replace("\x00", ""), {"section": section} if section else {}


def tabular_blocks(path):
    if path.suffix == ".csv":
        with path.open(encoding="utf-8-sig", newline="") as stream:
            for row, record in enumerate(csv.reader(stream, strict=True), 1):
                text = " | ".join(record)
                if text.strip(" |"):
                    yield text, {"cells": f"row {row}"}
        return

    from openpyxl import load_workbook
    from openpyxl.utils.cell import get_column_letter

    workbook = load_workbook(path, read_only=True, data_only=True, keep_links=False)
    count = 0
    try:
        for sheet in workbook:
            if sheet.max_row and sheet.max_column and sheet.max_row * sheet.max_column > MAX_CELLS:
                raise Rejected("too_many_cells")
            for row_number, row in enumerate(sheet.iter_rows(values_only=True), 1):
                count += len(row)
                if count > MAX_CELLS:
                    raise Rejected("too_many_cells")
                filled = [(i, val) for i, val in enumerate(row, 1) if val is not None and val != ""]
                if filled:
                    first, last = filled[0][0], filled[-1][0]
                    cells = f"{get_column_letter(first)}{row_number}:{get_column_letter(last)}{row_number}"
                    text = " | ".join(f"{get_column_letter(i)}: {val}" for i, val in filled)
                    yield text, {"sheet": sheet.title, "cells": cells}
    finally:
        workbook.close()


@lru_cache(maxsize=1)
def converter():
    from docling.datamodel.accelerator_options import AcceleratorDevice, AcceleratorOptions
    from docling.datamodel.base_models import InputFormat
    from docling.datamodel.pipeline_options import PdfPipelineOptions, TesseractCliOcrOptions
    from docling.document_converter import DocumentConverter, PdfFormatOption

    options = PdfPipelineOptions(
        do_ocr=True,
        do_table_structure=True,
        document_timeout=240,
        enable_remote_services=False,
        accelerator_options=AcceleratorOptions(device=AcceleratorDevice.CPU, num_threads=2),
        ocr_options=TesseractCliOcrOptions(lang=os.getenv("MAR_OCR_LANGUAGES", "spa,eng").split(",")),
    )
    return DocumentConverter(format_options={InputFormat.PDF: PdfFormatOption(pipeline_options=options)})


def docling_blocks(path):
    from docling.datamodel.base_models import ConversionStatus, InputFormat
    from docling_core.types.doc import SectionHeaderItem, TableItem, TextItem, TitleItem

    # Initialize the runtime separately so a missing model/cache is retryable.
    engine = converter()
    if path.suffix == ".pdf":
        engine.initialize_pipeline(InputFormat.PDF)
    result = engine.convert(path, raises_on_error=False, max_num_pages=MAX_PAGES)
    if result.status == ConversionStatus.PARTIAL_SUCCESS:
        raise TimeoutError("document conversion was partial")
    if result.status != ConversionStatus.SUCCESS:
        raise Rejected("document_conversion_failed")

    doc = result.document
    section = ""
    for item, _ in doc.iterate_items():
        if isinstance(item, (TitleItem, SectionHeaderItem)):
            section = item.text[:200]
        if isinstance(item, TableItem):
            text = item.export_to_markdown(doc=doc)
        elif isinstance(item, TextItem):
            text = item.text
        else:
            continue
        location = {"section": section} if section else {}
        spans = [(ref.page_no, *ref.charspan) for ref in item.prov if ref.page_no > 0]
        yield from located_blocks(text, location, spans, isinstance(item, TextItem))


def located_blocks(text, location, spans, text_item):
    pages = sorted({page for page, _, _ in spans})
    if len(pages) == 1:
        yield text, {**location, "page": pages[0]}
        return
    if not pages:
        yield text, location
        return

    # A Docling TextItem may join consecutive pages. Its character spans
    # identify each page's part of the original item text.
    ordered = sorted(spans, key=lambda span: span[1])
    if text_item and all(
        0 <= start < end <= len(text) and (i == 0 or start >= ordered[i-1][2])
        for i, (_, start, end) in enumerate(ordered)
    ):
        pieces = [text[start:end] for _, start, end in ordered]
        if "".join("".join(pieces).split()) == "".join(text.split()):
            for (page, _, _), piece in zip(ordered, pieces):
                yield piece, {**location, "page": page}
            return
    # A table or ambiguous range still has useful source pages without
    # falsely attributing all of its content to the first one.
    yield text, {**location, "pages": pages}


def extract(path):
    if path.suffix in {".docx", ".xlsx", ".pptx"}:
        with ZipFile(path) as archive:
            entries = archive.infolist()
            if len(entries) > 10_000 or sum(entry.file_size for entry in entries) > 256 << 20:
                raise Rejected("office_archive_too_large")
            if "[Content_Types].xml" not in archive.namelist():
                raise Rejected("invalid_office_document")
    if path.suffix in {".txt", ".md"}:
        return fragments(plain_blocks(path))
    if path.suffix in {".csv", ".xlsx"}:
        return fragments(tabular_blocks(path))
    return fragments(docling_blocks(path))
