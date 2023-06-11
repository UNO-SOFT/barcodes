// Copyright 2023 Tamás Gulácsi. All rights reserved.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"image"
	_ "image/jpeg"
	_ "image/png"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	pdfmodel "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"

	"github.com/UNO-SOFT/grcode"
	"github.com/tgulacsi/go/iohlp"
)

//go:generate sudo apt install libzbar-dev

func main() {
	if err := Main(); err != nil {
		log.Fatal(err)
	}
}

var pdfConf = pdfmodel.NewDefaultConfiguration()

func Main() error {
	flag.Parse()
	p := NewProcessor()
	defer p.Close()

	for _, fn := range flag.Args() {
		fh := os.Stdin
		if !(fn == "" || fn == "-") {
			var err error
			if fh, err = os.Open(fn); err != nil {
				return fmt.Errorf("%q: %w", fn, err)
			}
		}
		defer fh.Close()
		sr, err := iohlp.MakeSectionReader(fh, 1<<20)
		if err != nil {
			return err
		}
		results, err := p.Process(sr)
		if err != nil {
			return err
		}
		log.Println(results)
	}
	return nil
}

type Processor struct {
	scanner *grcode.Scanner
	pdfConf *pdfmodel.Configuration
}

func NewProcessor() Processor {
	return Processor{scanner: grcode.NewScanner(), pdfConf: pdfmodel.NewDefaultConfiguration()}
}
func (p Processor) Close() error {
	if p.scanner == nil {
		return nil
	}
	p.scanner.Close()
	return nil
}

func (p Processor) Process(rs io.ReadSeeker) (map[int][]string, error) {
	var a [16]byte
	if _, err := io.ReadAtLeast(rs, a[:], 8); err != nil {
		return nil, err
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if bytes.Equal(a[:7], []byte("%PDF-1.")) {
		return p.ProcessPDF(rs)
	}
	codes, err := p.ProcessImage(rs)
	if err != nil {
		return nil, err
	}
	return map[int][]string{1: codes}, nil
}

func (p Processor) ProcessPDF(rs io.ReadSeeker) (map[int][]string, error) {
	pdfCtx, err := pdfcpu.Read(rs, pdfConf)
	if err != nil {
		return nil, err
	}
	images, err := ExtractPageImages(pdfCtx)
	if err != nil {
		return nil, err
	}
	m := make(map[int][]string, len(images))
	for _, img := range images {
		results, err := p.ProcessImage(img)
		if err != nil {
			return m, err
		}
		m[img.PageNr] = append(m[img.PageNr], results...)
	}
	return m, nil
}

func (p Processor) ProcessImage(r io.Reader) ([]string, error) {
	/*
			scanner := NewScanner()
		defer scanner.Close()
		scanner.SetConfig(0, C.ZBAR_CFG_ENABLE, 1)
		zImg := NewZbarImage(image)
		defer zImg.Close()
		scanner.Scan(zImg)
		symbol := zImg.GetSymbol()
		for ; symbol != nil; symbol = symbol.Next() {
			results = append(results, symbol.Data())
		}
		return results, nil
	*/
	img, _, err := image.Decode(r)
	if err != nil {
		return nil, err
	}
	zImg := grcode.NewZbarImage(img)
	defer zImg.Close()
	var n int
	n, err = p.scanner.Scan(zImg)
	if err != nil {
		return nil, err
	} else if n == 0 {
		return nil, nil
	}
	results := make([]string, 0, n)
	symbol := zImg.GetSymbol()
	for ; symbol != nil; symbol = symbol.Next() {
		results = append(results, symbol.Data())
	}
	return results, nil
}

func ExtractPageImages(pdfCtx *pdfmodel.Context) ([]pdfmodel.Image, error) {
	if err := pdfCtx.EnsurePageCount(); err != nil {
		return nil, err
	}
	if err := pdfcpu.OptimizeXRefTable(pdfCtx); err != nil {
		return nil, err
	}
	var images []pdfmodel.Image
	for pageNr := 1; pageNr <= pdfCtx.XRefTable.PageCount; pageNr++ {
		for _, objNr := range pdfcpu.ImageObjNrs(pdfCtx, pageNr) {
			imageObj := pdfCtx.Optimize.ImageObjects[objNr]
			img, err := pdfcpu.ExtractImage(pdfCtx, imageObj.ImageDict, false, imageObj.ResourceNames[0], objNr, false)
			if err != nil {
				return images, err
			}
			if img != nil {
				img.PageNr = pageNr
				images = append(images, *img)
			}
		}
	}
	return images, nil
}
