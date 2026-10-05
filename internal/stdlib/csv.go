package stdlib

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.starlark.net/starlark"

	"calcside/internal/capability"
)

func delimiter(s string) (rune, error) {
	r, n := utf8.DecodeRuneInString(s)
	if n != len(s) || n == 0 || r == utf8.RuneError || r == 0 || r == '"' || r == '\r' || r == '\n' {
		return 0, errors.New("invalid delimiter")
	}
	return r, nil
}

func checkCSV(t *starlark.Thread, s string, sep rune) error {
	rows, cells, cols, field, steps := 0, 0, 1, 0, 0
	quoted, start, touched := false, true, false
	record := func() error {
		if touched {
			rows++
			cells += cols
			if rows > MaxRows || cells > MaxCells {
				return errors.New("CSV record or cell limit exceeded")
			}
		}
		cols, field, start, touched = 1, 0, true, false
		return nil
	}
	for i := 0; i < len(s); {
		steps++
		if steps&1023 == 0 {
			if err := checkpoint(t); err != nil {
				return err
			}
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		if quoted {
			if r == '"' {
				if i < len(s) && s[i] == '"' {
					i++
					field++
				} else {
					quoted = false
				}
			} else {
				field += n
			}
		} else if r == '\n' {
			if err := record(); err != nil {
				return err
			}
			continue
		} else if r == sep {
			cols++
			field = 0
			start = true
			touched = true
			if cols > MaxColumns {
				return errors.New("CSV column limit exceeded")
			}
			continue
		} else if r == '"' && start {
			quoted = true
			touched = true
			start = false
		} else {
			field += n
			start = false
			if r != '\r' {
				touched = true
			}
		}
		if field > MaxFieldBytes {
			return errors.New("CSV field limit exceeded")
		}
	}
	return record()
}

func csvParse(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var s string
	sep := ","
	if err := unpack(b, args, kwargs, "text", &s, "delimiter?", &sep); err != nil {
		return nil, err
	}
	if err := text(s, MaxBytes); err != nil {
		return nil, err
	}
	d, err := delimiter(sep)
	if err != nil {
		return nil, err
	}
	s = strings.TrimPrefix(s, "\ufeff")
	if err := checkCSV(t, s, d); err != nil {
		return nil, err
	}
	reader := csv.NewReader(strings.NewReader(s))
	reader.Comma = d
	rows := starlark.NewList(nil)
	var header []string
	records, cells := 0, 0
	for {
		if err := checkpoint(t); err != nil {
			return nil, err
		}
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			var p *csv.ParseError
			if errors.As(err, &p) {
				return nil, fmt.Errorf("invalid CSV at line %d column %d", p.Line, p.Column)
			}
			return nil, errors.New("invalid CSV")
		}
		records++
		cells += len(record)
		if records > MaxRows || len(record) > MaxColumns || cells > MaxCells {
			return nil, errors.New("CSV collection limit exceeded")
		}
		for _, v := range record {
			if len(v) > MaxFieldBytes {
				return nil, errors.New("CSV field limit exceeded")
			}
		}
		if b.Name() == "csv.parse_dicts" {
			if header == nil {
				header = record
				seen := map[string]bool{}
				for _, name := range header {
					if name == "" || seen[name] {
						return nil, errors.New("empty or duplicate CSV header")
					}
					seen[name] = true
				}
				continue
			}
			row := starlark.NewDict(len(header))
			for i, k := range header {
				_ = row.SetKey(starlark.String(k), starlark.String(record[i]))
			}
			_ = rows.Append(row)
		} else {
			_ = rows.Append(stringsValue(record))
		}
	}
	return rows, nil
}

func csvFormat(t *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var value, columns starlark.Value
	sep, safe := ",", false
	dicts := b.Name() == "csv.format_dicts"
	var err error
	if dicts {
		err = unpack(b, args, kwargs, "rows", &value, "columns", &columns, "delimiter?", &sep, "spreadsheet_safe?", &safe)
	} else {
		err = unpack(b, args, kwargs, "rows", &value, "delimiter?", &sep, "spreadsheet_safe?", &safe)
	}
	if err != nil {
		return nil, err
	}
	d, err := delimiter(sep)
	if err != nil {
		return nil, err
	}
	rows, err := sequence(value, MaxRows)
	if err != nil {
		return nil, err
	}
	out := &buffer{limit: MaxBytes}
	writer := csv.NewWriter(out)
	writer.Comma = d
	cells, input := 0, 0
	width := -1
	write := func(values []starlark.Value) error {
		cells += len(values)
		if cells > MaxCells {
			return errors.New("CSV cell limit exceeded")
		}
		if width < 0 {
			width = len(values)
		} else if width != len(values) {
			return errors.New("inconsistent CSV row width")
		}
		record := make([]string, len(values))
		for i, v := range values {
			s, ok := starlark.AsString(v)
			if !ok {
				return errors.New("CSV cells must be strings")
			}
			if err := text(s, MaxFieldBytes); err != nil {
				return err
			}
			input += len(s)
			if input > MaxBytes {
				return errors.New("input exceeds byte limit")
			}
			trimmed := strings.TrimLeftFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == '\ufeff' })
			if safe && len(s) > 0 && (strings.ContainsRune("\t\r\n", rune(s[0])) || len(trimmed) > 0 && strings.ContainsRune("=+-@", rune(trimmed[0]))) {
				if len(s) >= MaxFieldBytes {
					return errors.New("CSV field limit exceeded")
				}
				s = "'" + s
			}
			record[i] = s
		}
		if len(record) == 1 && record[0] == "" {
			writer.Flush()
			if writer.Error() != nil {
				return errors.New("CSV output exceeds byte limit")
			}
			return out.add("\"\"\n")
		}
		if err := writer.Write(record); err != nil {
			return errors.New("CSV output exceeds byte limit")
		}
		return nil
	}
	var header starlark.Indexable
	if dicts {
		header, err = sequence(columns, MaxColumns)
		if err != nil {
			return nil, err
		}
		if header.Len() == 0 || rows.Len() >= MaxRows {
			return nil, errors.New("invalid CSV header or record limit exceeded")
		}
		names := make([]starlark.Value, header.Len())
		seen := map[string]bool{}
		for i := range names {
			name, ok := starlark.AsString(header.Index(i))
			if !ok || name == "" || seen[name] {
				return nil, errors.New("empty or duplicate CSV header")
			}
			seen[name] = true
			names[i] = header.Index(i)
		}
		if err := write(names); err != nil {
			return nil, err
		}
	}
	for i := 0; i < rows.Len(); i++ {
		if err := checkpoint(t); err != nil {
			return nil, err
		}
		var values []starlark.Value
		if dicts {
			row, ok := rows.Index(i).(*starlark.Dict)
			if !ok {
				return nil, errors.New("expected dict rows")
			}
			values = make([]starlark.Value, header.Len())
			for j := range values {
				v, found, err := row.Get(header.Index(j))
				if err != nil || !found {
					return nil, errors.New("missing CSV column")
				}
				values[j] = v
			}
		} else {
			row, err := sequence(rows.Index(i), MaxColumns)
			if err != nil {
				return nil, err
			}
			if row.Len() == 0 {
				return nil, errors.New("CSV rows must contain at least one column")
			}
			values = make([]starlark.Value, row.Len())
			for j := range values {
				values[j] = row.Index(j)
			}
		}
		if err := write(values); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if writer.Error() != nil {
		return nil, errors.New("CSV output exceeds byte limit")
	}
	return starlark.String(out.String()), nil
}

func CheckCSVLimits(ctx context.Context, s string, sep rune) error {
	if err := text(s, MaxBytes); err != nil {
		return err
	}
	if _, err := delimiter(string(sep)); err != nil {
		return err
	}
	thread := &starlark.Thread{}
	thread.SetLocal(capability.ContextKey, ctx)
	return checkCSV(thread, strings.TrimPrefix(s, "\ufeff"), sep)
}
