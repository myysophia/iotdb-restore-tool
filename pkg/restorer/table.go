package restorer

import "strings"

type cliTableRow map[string]string

func parseCLITable(output string) []cliTableRow {
	lines := splitLines(output)
	tableLines := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "|") {
			tableLines = append(tableLines, trimmed)
		}
	}

	if len(tableLines) < 2 {
		return nil
	}

	headers := parseCLIColumns(tableLines[0])
	if len(headers) == 0 {
		return nil
	}

	rows := make([]cliTableRow, 0, len(tableLines)-1)
	for _, line := range tableLines[1:] {
		columns := parseCLIColumns(line)
		if len(columns) != len(headers) {
			continue
		}

		row := make(cliTableRow, len(headers))
		for i, header := range headers {
			row[header] = columns[i]
		}
		rows = append(rows, row)
	}

	return rows
}

func parseCLIColumns(line string) []string {
	if !strings.HasPrefix(line, "|") {
		return nil
	}

	rawParts := strings.Split(line, "|")
	parts := make([]string, 0, len(rawParts))
	for _, part := range rawParts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		parts = append(parts, part)
	}
	return parts
}
