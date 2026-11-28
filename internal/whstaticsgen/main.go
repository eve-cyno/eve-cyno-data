// Command whstaticsgen converts Pathfinder's system_static.csv (MIT) to whstatics/statics.json.
//
//	go run ./internal/whstaticsgen -csv system_static.csv -out whstatics/statics.json
//
// The CSV is read from a local file (download it from the pinned commit named in the
// whstatics package doc); the command makes no network calls.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"

	"eve-cyno.dev/go/data/whstatics"
)

const pinned = "d45fa9b527bc81b28960ca3744264e7441fe6484"

func main() {
	in := flag.String("csv", "", "path of system_static.csv")
	out := flag.String("out", "whstatics/statics.json", "output file")
	flag.Parse()
	if *in == "" {
		fmt.Fprintln(os.Stderr, "whstaticsgen: -csv is required")
		os.Exit(2)
	}
	f, err := os.Open(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "whstaticsgen:", err)
		os.Exit(1)
	}
	defer f.Close()
	d, err := convert(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "whstaticsgen:", err)
		os.Exit(1)
	}
	b, err := json.MarshalIndent(d, "", " ")
	if err == nil {
		err = os.WriteFile(*out, append(b, '\n'), 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "whstaticsgen:", err)
		os.Exit(1)
	}
}

// convert reads the "id;systemId;typeId" CSV into a dataset with the provenance fields.
func convert(f *os.File) (*whstatics.Dataset, error) {
	r := csv.NewReader(f)
	r.Comma = ';'
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) < 2 || len(rows[0]) != 3 || rows[0][1] != "systemId" || rows[0][2] != "typeId" {
		return nil, fmt.Errorf("unexpected header, want id;systemId;typeId")
	}
	by := map[int][]int{}
	for i, row := range rows[1:] {
		sys, err1 := strconv.Atoi(row[1])
		typ, err2 := strconv.Atoi(row[2])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("row %d: not numeric: %v", i+2, row)
		}
		by[sys] = append(by[sys], typ)
	}
	d := &whstatics.Dataset{
		SchemaVersion: whstatics.SchemaVersion,
		Source:        "Pathfinder export/csv/system_static.csv (https://github.com/exodus4d/pathfinder)",
		SourceURL:     "https://github.com/exodus4d/pathfinder/blob/" + pinned + "/export/csv/system_static.csv",
		License:       "MIT",
		LicenseURL:    "https://github.com/exodus4d/pathfinder/blob/" + pinned + "/LICENSE",
		Credit:        "Copyright (c) 2017 Mark Friedrich (Pathfinder)",
		DataDate:      "2019-09-10",
		Note:          "Data from 2019; may miss shattered systems and statics that changed since.",
	}
	for sys, types := range by {
		sort.Ints(types)
		d.Systems = append(d.Systems, whstatics.System{SystemID: sys, Statics: types})
	}
	sort.Slice(d.Systems, func(i, j int) bool { return d.Systems[i].SystemID < d.Systems[j].SystemID })
	return d, nil
}
