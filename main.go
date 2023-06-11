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
	"time"

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
func Main() error {
	flag.Parse()
	scanner := grcode.NewScanner()
	defer scanner.Close()

	conf := pdfmodel.NewDefaultConfiguration()
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
		var a [16]byte
		if _, err := io.ReadAtLeast(
			io.NewSectionReader(sr, 0, sr.Size()), a[:], 8,
		); err != nil {
			return fmt.Errorf("read from %q: %w", fh.Name(), err)
		}
		if bytes.Equal(a[:7], []byte("%PDF-1.")) {
			pdfCtx, err := pdfcpu.Read(sr, conf)
			if err != nil {
				return err
			}
			images, err := ExtractPageImages(pdfCtx)
			if err != nil {
				return err
			}
			for _, img := range images {
				results, err := processImage(scanner, img)
				if err != nil {
					return err
				}
				log.Println(results)
			}
		} else {
			results, err := processImage(scanner, sr)
			if err != nil {
				return err
			}
			log.Println(results)
		}
	}
	return nil
}

func processImage(scanner *grcode.Scanner, r io.Reader) ([]string, error) {
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
	img, format, err := image.Decode(r)
	if err != nil {
		return nil, err
	}
	log.Println(format)
	zImg := grcode.NewZbarImage(img)
	defer zImg.Close()
	var n int
	start := time.Now()
	n, err = scanner.Scan(zImg)
	dur := time.Since(start)
	log.Printf("scan %v: %+v (%s)", format, err, dur)
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
			log.Println(objNr)
			imageObj := pdfCtx.Optimize.ImageObjects[objNr]
			img, err := pdfcpu.ExtractImage(pdfCtx, imageObj.ImageDict, false, imageObj.ResourceNames[0], objNr, false)
			if err != nil {
				return images, err
			}
			if img != nil {
				img.PageNr = pageNr
				images = append(images, *img)
				fmt.Printf("img: %v\n", img)
			}
		}
	}
	return images, nil
}
