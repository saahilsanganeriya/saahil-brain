package parse

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"path"
	"strconv"
	"strings"
)

const shapeKeys = 12

// Shape describes a data file from its first bytes (scan caps them at 64 KB): top-level keys and counts for json,
// the first record's keys for jsonl, the header for csv/tsv, the name alone for parquet.
func Shape(name string, data []byte) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".json":
		return jsonShape(data)
	case ".jsonl", ".ndjson":
		line, _, complete := bytes.Cut(data, []byte("\n"))
		if !complete && len(data) >= 64<<10 {
			return "jsonl: first record > 64 KB"
		}
		keys, ok := objectKeys(line)
		if !ok {
			return "jsonl: first record is not an object"
		}
		return "jsonl: first record keys: " + keyList(keys)
	case ".csv", ".tsv":
		r := csv.NewReader(bytes.NewReader(data))
		r.LazyQuotes, r.FieldsPerRecord = true, -1
		if strings.HasSuffix(strings.ToLower(name), ".tsv") {
			r.Comma = '\t'
		}
		head, err := r.Read()
		if err != nil {
			return "csv: unreadable header"
		}
		return "csv: " + strconv.Itoa(len(head)) + " columns: " + keyList(head)
	case ".parquet":
		return "parquet"
	}
	return ""
}

func jsonShape(data []byte) string {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return "json: unreadable"
	}
	delim, _ := tok.(json.Delim)
	switch delim {
	case '{':
		keys, complete := objectKeys(data)
		if complete {
			return "json object: " + strconv.Itoa(len(keys)) + " keys: " + keyList(keys)
		}
		return "json object: ≥" + strconv.Itoa(len(keys)) + " keys (cut at 64 KB): " + keyList(keys)
	case '[':
		n, complete, first := 0, false, []string(nil)
		for dec.More() {
			var item json.RawMessage
			if dec.Decode(&item) != nil {
				break
			}
			if n == 0 {
				first, _ = objectKeys(item)
			}
			n++
		}
		if t, err := dec.Token(); err == nil && t == json.Delim(']') {
			complete = true
		}
		count := strconv.Itoa(n)
		if !complete {
			count = "≥" + count
		}
		s := "json array: " + count + " items"
		if len(first) > 0 {
			s += ", first keys: " + keyList(first)
		}
		return s
	}
	return "json scalar"
}

// objectKeys returns an object's top-level keys in file order (map decoding would sort them) and whether the
// object closed before the data ran out.
func objectKeys(obj []byte) ([]string, bool) {
	dec := json.NewDecoder(bytes.NewReader(obj))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	var keys []string
	for {
		k, err := dec.Token()
		if err != nil {
			return keys, false
		}
		if k == json.Delim('}') {
			return keys, true
		}
		keys = append(keys, k.(string))
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return keys, false
		}
	}
}

func keyList(keys []string) string {
	if len(keys) > shapeKeys {
		return strings.Join(keys[:shapeKeys], ", ") + ", …"
	}
	return strings.Join(keys, ", ")
}
