#!/usr/bin/env python3
"""Детерминированные безвредные офисные документы для AV/CDR integration tests."""
from pathlib import Path
import importlib.util
import subprocess
import zipfile

spec = importlib.util.spec_from_file_location("files_local", Path(__file__).with_name("local.py"))
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)
folder = fixture.STATE / "documents"
folder.mkdir(mode=0o700, exist_ok=True)
namespaces = '''xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" xmlns:draw="urn:oasis:names:tc:opendocument:xmlns:drawing:1.0" xmlns:style="urn:oasis:names:tc:opendocument:xmlns:style:1.0" xmlns:svg="urn:oasis:names:tc:opendocument:xmlns:svg-compatible:1.0" xmlns:fo="urn:oasis:names:tc:opendocument:xmlns:xsl-fo-compatible:1.0"'''
documents = {
    "odt": ("text", "<office:text><text:p>MarketMesh MM-43 document</text:p></office:text>"),
    "ods": ("spreadsheet", '<office:spreadsheet><table:table table:name="Sheet1"><table:table-row><table:table-cell office:value-type="string"><text:p>MarketMesh MM-43 sheet</text:p></table:table-cell></table:table-row></table:table></office:spreadsheet>'),
    "odp": ("presentation", '<office:presentation><draw:page draw:name="Slide1" draw:master-page-name="Default"><draw:frame svg:x="2cm" svg:y="2cm" svg:width="15cm" svg:height="5cm"><draw:text-box><text:p>MarketMesh MM-43 slide</text:p></draw:text-box></draw:frame></draw:page></office:presentation>'),
}
for extension, (kind, body) in documents.items():
    mime = "application/vnd.oasis.opendocument." + kind
    content = f'<?xml version="1.0" encoding="UTF-8"?><office:document-content {namespaces} office:version="1.3"><office:automatic-styles/><office:body>{body}</office:body></office:document-content>'
    styles = f'<?xml version="1.0" encoding="UTF-8"?><office:document-styles {namespaces} office:version="1.3"><office:styles/><office:automatic-styles><style:page-layout style:name="PM1"><style:page-layout-properties fo:page-width="21cm" fo:page-height="29.7cm"/></style:page-layout></office:automatic-styles><office:master-styles><style:master-page style:name="Default" style:page-layout-name="PM1"/></office:master-styles></office:document-styles>'
    manifest = f'<?xml version="1.0"?><manifest:manifest xmlns:manifest="urn:oasis:names:tc:opendocument:xmlns:manifest:1.0" manifest:version="1.3"><manifest:file-entry manifest:full-path="/" manifest:media-type="{mime}"/><manifest:file-entry manifest:full-path="content.xml" manifest:media-type="text/xml"/><manifest:file-entry manifest:full-path="styles.xml" manifest:media-type="text/xml"/></manifest:manifest>'
    with zipfile.ZipFile(folder / ("sample." + extension), "w", compression=zipfile.ZIP_STORED) as archive:
        for name, data in (("mimetype", mime), ("content.xml", content), ("styles.xml", styles), ("META-INF/manifest.xml", manifest)):
            archive.writestr(name, data)

for source, target, filter_name in (("odt", "docx", "Office Open XML Text"), ("ods", "xlsx", "Calc MS Excel 2007 XML"), ("odp", "pptx", "Impress MS PowerPoint 2007 XML")):
    result = subprocess.run([
        "docker", "run", "--rm", "--label", "marketmesh.task=MM-43", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--memory", "1g", "--cpus", "1", "--pids-limit", "128", "--user", f"{fixture.ENV['MM_FILES_UID']}:{fixture.ENV['MM_FILES_GID']}",
        "--tmpfs", "/tmp:rw,nosuid,size=128m,mode=1777", "--mount", f"type=bind,src={folder},dst=/documents", "--entrypoint", "/usr/bin/libreoffice", "marketmesh-mm43-sandbox:local", "-env:UserInstallation=file:///tmp/profile", "--headless", "--convert-to", target + ":" + filter_name, "--outdir", "/documents", "/documents/sample." + source,
    ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=60)
    if result.returncode or not (folder / ("sample." + target)).is_file():
        raise SystemExit("MM-43: создание офисного fixture не завершено")
print("MM-43: созданы шесть офисных fixtures без внешних данных.")
