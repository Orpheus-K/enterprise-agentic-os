"""Bounded text extraction; process isolation remains a separate required boundary."""

import io
import json
import zipfile
from collections.abc import Callable
from pathlib import PurePosixPath
from xml.etree import ElementTree

from docx import Document
from docx.oxml.exceptions import InvalidXmlError
from docx.text.paragraph import Paragraph
from pypdf import PdfReader, apply_configuration
from pypdf.errors import PyPdfError

PDF = "application/pdf"
DOCX = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
MAX_INPUT = 8 * 1024 * 1024
MAX_OUTPUT = 1024 * 1024
MAX_EXPANDED = 16 * 1024 * 1024
MAX_BLOCKS = 10000


class DocumentError(ValueError):
    """A stable, non-content-bearing rejection reason."""


def extract(data: bytes, media_type: str, check_active: Callable[[], None]) -> bytes:
    """Return canonical JSON text blocks, without filesystem or network access."""
    check_active()
    if not data or len(data) > MAX_INPUT:
        raise DocumentError("input_size")
    blocks: list[str] = []
    size = 0

    def append(text: str) -> None:
        nonlocal size
        check_active()
        size += len(text.encode("utf-8"))
        if size > MAX_OUTPUT or len(blocks) >= MAX_BLOCKS:
            raise DocumentError("output_size")
        blocks.append(text)

    try:
        if media_type == PDF:
            _pdf(data, append, check_active)
        elif media_type == DOCX:
            _docx(data, append, check_active)
        else:
            raise DocumentError("unsupported_media_type")
    except (PyPdfError, zipfile.BadZipFile, ElementTree.ParseError, InvalidXmlError) as error:
        raise DocumentError("malformed_document") from error
    check_active()
    result = json.dumps(
        {"format": "agentos.document-text.v1", "blocks": blocks},
        ensure_ascii=False,
        separators=(",", ":"),
    ).encode("utf-8")
    if len(result) > MAX_OUTPUT:
        raise DocumentError("output_size")
    return result


def _pdf(data: bytes, append: Callable[[str], None], check: Callable[[], None]) -> None:
    if not data.startswith(b"%PDF-"):
        raise DocumentError("malformed_document")
    with apply_configuration(
        maximum_declared_stream_length=MAX_EXPANDED,
        zlib_maximum_output_length=MAX_EXPANDED,
        lzw_maximum_output_length=MAX_EXPANDED,
        run_length_maximum_output_length=MAX_EXPANDED,
        array_based_stream_maximum_output_length=MAX_EXPANDED,
        stream_decoding_work_maximum_length=MAX_EXPANDED,
        page_tree_maximum_entries=1000,
        jbig2dec_binary=None,
    ):
        reader = PdfReader(io.BytesIO(data), strict=True)
        if reader.is_encrypted:
            raise DocumentError("encrypted_document")
        root = reader.root_object
        if any(key in root for key in ("/OpenAction", "/AA", "/AcroForm")):
            raise DocumentError("active_document")
        names = root.get("/Names")
        if names is not None:
            names = names.get_object()
            if any(key in names for key in ("/JavaScript", "/EmbeddedFiles")):
                raise DocumentError("active_document")
        if len(reader.pages) > 100:
            raise DocumentError("page_limit")
        for page in reader.pages:
            check()
            if "/AA" in page or "/Annots" in page:
                raise DocumentError("active_document")
            text = page.extract_text()
            if not text.strip() and "/XObject" in page.get("/Resources", {}):
                raise DocumentError("ocr_required")
            append(text)


def _docx(data: bytes, append: Callable[[str], None], check: Callable[[], None]) -> None:
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        entries = archive.infolist()
        if len(entries) > 1024 or sum(item.file_size for item in entries) > MAX_EXPANDED:
            raise DocumentError("archive_size")
        names: set[str] = set()
        for item in entries:
            check()
            path = PurePosixPath(item.filename)
            if (
                item.filename in names
                or path.is_absolute()
                or ".." in path.parts
                or "\\" in item.filename
                or item.flag_bits & 1
                or "embeddings" in path.parts
                or "vbaproject" in item.filename.lower()
            ):
                raise DocumentError("unsafe_archive")
            names.add(item.filename)
            if item.filename.endswith((".xml", ".rels")):
                xml = archive.read(item)
                try:
                    source = xml.decode("utf-8-sig")
                except UnicodeDecodeError as error:
                    raise DocumentError("unsupported_xml_encoding") from error
                if (
                    "\x00" in source
                    or "<!DOCTYPE" in source.upper()
                    or "<!ENTITY" in source.upper()
                ):
                    raise DocumentError("unsafe_xml")
                tree = ElementTree.fromstring(xml)
                if item.filename.endswith(".rels") and any(
                    node.attrib.get("TargetMode", "").lower() == "external" for node in tree.iter()
                ):
                    raise DocumentError("external_relationship")
        if "word/document.xml" not in names:
            raise DocumentError("malformed_document")
    document = Document(io.BytesIO(data))
    for block in document.iter_inner_content():
        if isinstance(block, Paragraph):
            append(block.text)
        else:
            for row in block.rows:
                for cell in row.cells:
                    append(cell.text)
