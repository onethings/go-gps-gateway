package report

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Sheet is one worksheet of an export workbook.
type Sheet struct {
	Name string
	Rows [][]any
}

// WriteXLSX writes a minimal Office Open XML workbook. It supports what the
// reports need: several sheets of inline strings and numbers, no styles, no
// formulas. The Java server renders an XLSX template; this is the dependency
// free subset that produces a file every spreadsheet program opens.
func WriteXLSX(w io.Writer, sheets []Sheet) error {
	if len(sheets) == 0 {
		sheets = []Sheet{{Name: "Report"}}
	}

	archive := zip.NewWriter(w)
	entries := []struct {
		name    string
		content string
	}{
		{"[Content_Types].xml", contentTypes(len(sheets))},
		{"_rels/.rels", rootRels()},
		{"xl/workbook.xml", workbook(sheets)},
		{"xl/_rels/workbook.xml.rels", workbookRels(len(sheets))},
	}
	for _, entry := range entries {
		if err := writeZipEntry(archive, entry.name, entry.content); err != nil {
			return err
		}
	}
	for index, sheet := range sheets {
		if err := writeZipEntry(archive,
			fmt.Sprintf("xl/worksheets/sheet%d.xml", index+1), worksheet(sheet)); err != nil {
			return err
		}
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("report: xlsx: %w", err)
	}
	return nil
}

func writeZipEntry(archive *zip.Writer, name, content string) error {
	file, err := archive.Create(name)
	if err != nil {
		return fmt.Errorf("report: xlsx %s: %w", name, err)
	}
	if _, err := io.WriteString(file, content); err != nil {
		return fmt.Errorf("report: xlsx %s: %w", name, err)
	}
	return nil
}

func contentTypes(sheetCount int) string {
	var builder strings.Builder
	builder.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	builder.WriteString(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`)
	builder.WriteString(`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>`)
	builder.WriteString(`<Default Extension="xml" ContentType="application/xml"/>`)
	builder.WriteString(`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`)
	for index := 1; index <= sheetCount; index++ {
		fmt.Fprintf(&builder, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, index)
	}
	builder.WriteString(`</Types>`)
	return builder.String()
}

func rootRels() string {
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
		`</Relationships>`
}

func workbook(sheets []Sheet) string {
	var builder strings.Builder
	builder.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	builder.WriteString(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	seen := map[string]int{}
	for index, sheet := range sheets {
		name := sanitizeSheetName(sheet.Name, seen)
		fmt.Fprintf(&builder, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`,
			xmlEscape(name), index+1, index+1)
	}
	builder.WriteString(`</sheets></workbook>`)
	return builder.String()
}

func workbookRels(sheetCount int) string {
	var builder strings.Builder
	builder.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	builder.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for index := 1; index <= sheetCount; index++ {
		fmt.Fprintf(&builder, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, index, index)
	}
	builder.WriteString(`</Relationships>`)
	return builder.String()
}

func worksheet(sheet Sheet) string {
	var builder strings.Builder
	builder.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	builder.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for rowIndex, row := range sheet.Rows {
		fmt.Fprintf(&builder, `<row r="%d">`, rowIndex+1)
		for columnIndex, cell := range row {
			reference := columnName(columnIndex) + strconv.Itoa(rowIndex+1)
			switch typed := cell.(type) {
			case nil:
				continue
			case float64:
				fmt.Fprintf(&builder, `<c r="%s"><v>%s</v></c>`, reference, formatNumber(typed))
			case float32:
				fmt.Fprintf(&builder, `<c r="%s"><v>%s</v></c>`, reference, formatNumber(float64(typed)))
			case int:
				fmt.Fprintf(&builder, `<c r="%s"><v>%d</v></c>`, reference, typed)
			case int64:
				fmt.Fprintf(&builder, `<c r="%s"><v>%d</v></c>`, reference, typed)
			case bool:
				fmt.Fprintf(&builder, `<c r="%s" t="b"><v>%d</v></c>`, reference, boolValue(typed))
			case time.Time:
				fmt.Fprintf(&builder, `<c r="%s" t="inlineStr"><is><t>%s</t></is></c>`,
					reference, xmlEscape(formatTime(typed)))
			default:
				fmt.Fprintf(&builder, `<c r="%s" t="inlineStr"><is><t>%s</t></is></c>`,
					reference, xmlEscape(fmt.Sprint(cell)))
			}
		}
		builder.WriteString(`</row>`)
	}
	builder.WriteString(`</sheetData></worksheet>`)
	return builder.String()
}

func boolValue(value bool) int {
	if value {
		return 1
	}
	return 0
}

// columnName turns a zero based index into A, B, ... AA, AB, ...
func columnName(index int) string {
	name := ""
	for index >= 0 {
		name = string(rune('A'+index%26)) + name
		index = index/26 - 1
	}
	return name
}

// sanitizeSheetName keeps a name unique and inside the 31 character limit the
// format imposes.
func sanitizeSheetName(name string, seen map[string]int) string {
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case '[', ']', '*', '/', '\\', '?', ':':
			return '-'
		default:
			return r
		}
	}, strings.TrimSpace(name))
	if cleaned == "" {
		cleaned = "Report"
	}
	if len(cleaned) > 31 {
		cleaned = cleaned[:31]
	}
	seen[cleaned]++
	if count := seen[cleaned]; count > 1 {
		suffix := "-" + strconv.Itoa(count)
		if len(cleaned)+len(suffix) > 31 {
			cleaned = cleaned[:31-len(suffix)]
		}
		cleaned += suffix
	}
	return cleaned
}

// xmlEscape escapes a text value for XML content.
func xmlEscape(value string) string {
	var buffer strings.Builder
	if err := xml.EscapeText(&buffer, []byte(value)); err != nil {
		return value
	}
	return buffer.String()
}
