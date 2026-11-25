import tempfile
import unittest
from pathlib import Path

from documents import MAX_CHARS, Rejected, extract, located_blocks, split_text


class FragmentTests(unittest.TestCase):
    def test_docling_joined_text_keeps_each_source_page(self):
        blocks = list(located_blocks("Primera Segunda", {}, [(1, 0, 7), (2, 8, 15)], True))
        self.assertEqual(blocks, [("Primera", {"page": 1}), ("Segunda", {"page": 2})])

    def test_ambiguous_ranges_report_all_pages(self):
        blocks = list(located_blocks("tabla", {}, [(1, 0, 5), (2, 0, 5)], False))
        self.assertEqual(blocks, [("tabla", {"pages": [1, 2]})])

    def test_csv_preserves_source_row(self):
        with tempfile.TemporaryDirectory() as temp:
            source = Path(temp) / "data.csv"
            source.write_text('Concepto,Coste\n"Dieta, viaje",55\n', encoding="utf-8")
            self.assertEqual(extract(source)[1], {"text": "Dieta, viaje | 55", "cells": "row 2"})

    def test_long_text_is_split_without_losing_words(self):
        text = "  " + "versión " * 800
        parts = list(split_text(text))
        self.assertTrue(all(0 < len(part) <= MAX_CHARS for part in parts))
        self.assertEqual(" ".join(parts).split(), text.split())

    def test_empty_document_is_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            source = Path(temp) / "empty.txt"
            source.write_text("\n", encoding="utf-8")
            with self.assertRaisesRegex(Rejected, "no_extractable_text"):
                extract(source)


if __name__ == "__main__":
    unittest.main()
