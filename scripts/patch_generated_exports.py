"""Keep the local Arrow C Data compatibility fix when regenerating driverbase exports."""

from pathlib import Path

driver = Path(__file__).resolve().parents[1] / "go/pkg/hardcharger/driver.go"
source = driver.read_text()
original = "cdata.ExportArrowRecordBatch(cStream.rdr.RecordBatch(), toCdataArray(array), nil)"
replacement = "exportRecordBatch(cStream.rdr.RecordBatch(), toCdataArray(array))"
if source.count(original) != 1:
    raise SystemExit("driverbase export template changed: review the extension-array export fix")
driver.write_text(source.replace(original, replacement))
