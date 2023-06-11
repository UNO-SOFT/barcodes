// Copyright 2023 Tamás Gulácsi. All rights reserved.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/zip"
	"encoding/csv"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tgulacsi/go/iohlp"
	"golang.org/x/sync/errgroup"
)

func TestPDF(t *testing.T) {
	cfh, err := os.Open(filepath.Join("testdata", "karscn.csv"))
	if err != nil {
		t.Skip(err)
	}
	defer cfh.Close()
	cr := csv.NewReader(cfh)
	cr.FieldsPerRecord = 2
	want := make(map[string]string)
	for row, err := cr.Read(); err == nil; row, err = cr.Read() {
		nm, _ := strings.CutPrefix(row[1], "karscn/")
		want[nm] = row[0]
	}
	cfh.Close()

	zfh, err := os.Open(filepath.Join("testdata", "karscn.zip"))
	if err != nil {
		t.Skip(err)
	}
	defer zfh.Close()
	size, _ := zfh.Seek(0, 2)
	_, _ = zfh.Seek(0, 0)
	zr, err := zip.NewReader(zfh, size)
	if err != nil {
		t.Fatal(err)
	}

	processors := make(chan Processor, runtime.GOMAXPROCS(-1))
	defer func() {
		close(processors)
		for p := range processors {
			p.Close()
		}
	}()
	var grp errgroup.Group
	conc := runtime.GOMAXPROCS(-1)
	grp.SetLimit(conc)

	for remainder := 0; remainder < conc; remainder++ {
		remainder := remainder
		grp.Go(func() error {
		Loop:
			for i, f := range zr.File {
				if i%conc != remainder {
					continue
				}
				ext := strings.ToLower(filepath.Ext(f.Name))
				switch ext {
				case ".pdf", ".jpg", ".jpeg", ".png":
				default:
					continue Loop
				}
				t.Run(f.Name, func(t *testing.T) {
					rc, err := f.Open()
					if err != nil {
						t.Fatal(err)
					}
					defer rc.Close()

					w := want[f.Name[:len(f.Name)-len(ext)]]
					sr, err := iohlp.MakeSectionReader(rc, 1<<20)
					if err != nil {
						t.Fatal(err)
					}
					var p Processor
					select {
					case p = <-processors:
					default:
						p = NewProcessor()
					}
					defer func() {
						select {
						case processors <- p:
						default:
							p.Close()
						}
					}()
					codes, err := p.Process(sr)
					if err != nil {
						t.Fatal(err)
					}
					t.Logf("%q codes: %v", t.Name(), codes)
					var n int
					var found bool
					for i, cs := range codes {
						if len(cs) == 0 {
							t.Logf("%q page %d is empty", t.Name(), i)
							continue
						}
						for _, c := range cs {
							if !found {
								found = c == w
							}
						}
						n += len(cs)
					}
					if n == 0 {
						t.Errorf("%q: no codes", t.Name())
					}
					if w != "" && !found {
						t.Fatalf("%q code %q not found", t.Name(), w)
					}
				})
			}
			return nil
		})
	}
	if err := grp.Wait(); err != nil {
		t.Fatal(err)
	}
}
