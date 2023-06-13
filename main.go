// Copyright 2023 Tamás Gulácsi. All rights reserved.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	pnm "github.com/UNO-SOFT/gopnm"
	"golang.org/x/image/draw"
	"image"
	_ "image/jpeg"
	"image/png"

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
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
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
		results, err := p.GetBarcodes(ctx, sr)
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

func (p Processor) ProcessImages(ctx context.Context, rs io.ReadSeeker, fun func(context.Context, io.Reader) error) error {
	var a [16]byte
	if _, err := io.ReadAtLeast(rs, a[:], 8); err != nil {
		return err
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var images []pdfmodel.Image
	if !bytes.Equal(a[:7], []byte("%PDF-1.")) {
		images = append(images, pdfmodel.Image{Reader: rs, PageNr: 0})
	} else {
		pdfCtx, err := pdfcpu.Read(rs, pdfConf)
		if err != nil {
			return err
		}
		images, err = ExtractPageImages(pdfCtx)
		if err != nil {
			return err
		}
	}

	for _, img := range images {
		if err := fun(ctx, img); err != nil {
			return err
		}
	}
	return nil
}

func (p Processor) GetBarcodes(ctx context.Context, rs io.ReadSeeker) (map[int][]string, error) {
	m := make(map[int][]string)
	var idx int
	err := p.ProcessImages(ctx, rs, func(ctx context.Context, img io.Reader) error {
		codes, err := p.ProcessImage(ctx, img)
		idx--
		nr := idx
		if pi, ok := img.(pdfmodel.Image); ok {
			nr = pi.PageNr
		}
		m[nr] = append(m[nr], codes...)
		return err
	})
	return m, err

}

func (p Processor) ProcessPDF(ctx context.Context, rs io.ReadSeeker) (map[int][]string, error) {
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
		results, err := p.ProcessImage(ctx, img)
		if err != nil {
			return m, err
		}
		m[img.PageNr] = append(m[img.PageNr], results...)
	}
	return m, nil
}

func (p Processor) ProcessImage(ctx context.Context, r io.Reader) ([]string, error) {
	hsh := sha256.New()
	img, _, err := image.Decode(io.TeeReader(r, hsh))
	if err != nil {
		return nil, err
	}
	hshS := base64.URLEncoding.EncodeToString(hsh.Sum(nil))
	//log.Println("hash:", hshS, "bounds:", img.Bounds(), "boxes:", boxes(img.Bounds()))
	var results []string

	if IsEmpty(img) {
		//log.Println("hshS is empty")
		fh, _ := os.Create("/tmp/" + hshS + "-empty.png")
		png.Encode(fh, img)
		fh.Close()
		return nil, nil
	}
	if results, err = p.scanImage(results, img); err != nil {
		log.Println(err)
	} else if len(results) != 0 {
		return results, nil
	}
	if img, err = Unpaper(ctx, img); err != nil {
		log.Printf("unpaper %s: %+v", hshS, err)
	}
	for _, box := range boxes(img.Bounds()) {
		if err := func() error {
			sub := img.(interface {
				SubImage(image.Rectangle) image.Image
			}).SubImage(box)
			var err error
			results, err = p.scanImage(results, sub)
			return err
		}(); err != nil || len(results) != 0 {
			return results, err
		}
	}
	return results, nil
}

// scanImage scans the barcode from one code.
func (p Processor) scanImage(results []string, img image.Image) ([]string, error) {
	zImg := grcode.NewZbarImage(img)
	defer zImg.Close()
	n, err := p.scanner.Scan(zImg)
	if err != nil {
		return results, err
	} else if n == 0 {
		return results, nil
	}
	results = make([]string, 0, n)
	symbol := zImg.GetSymbol()
	for ; symbol != nil; symbol = symbol.Next() {
		results = append(results, symbol.Data())
	}
	return results, nil
}

var unpaperExecOnce sync.Once
var unpaperExec string

// Unpaper calls unpaper on the image.
func Unpaper(ctx context.Context, img image.Image) (image.Image, error) {
	unpaperExecOnce.Do(func() {
		var err error
		if unpaperExec, err = exec.LookPath("unpaper"); err != nil {
			log.Println(err)
		}
	})
	if unpaperExec == "" {
		return img, nil
	}
	in, err := os.CreateTemp("", "barcode-in-*.pgm")
	if err != nil {
		return img, err
	}
	defer func() { in.Close(); os.Remove(in.Name()) }()
	if err = pnm.Encode(in, img, pnm.PGM); err != nil {
		return img, err
	}

	outBase := strings.TrimSuffix(in.Name(), ".pgm") + "-out-"
	outMask, outFn := outBase+"%03d.pbm", outBase+"001.pbm"
	defer os.Remove(outFn)
	var errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, unpaperExec, "-t", "pbm", "-b", "0.5", in.Name(), outMask)
	log.Println(cmd.Args)
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return img, fmt.Errorf("%s: %w", errBuf.String(), err)
	}
	//log.Println(errBuf.String())
	out, err := os.Open(outFn)
	if err != nil {
		return img, err
	}
	defer out.Close()
	return pnm.Decode(out)
}

func boxes(bounds image.Rectangle) []image.Rectangle {
	bb := make([]image.Rectangle, 1, 16)
	bb[0] = bounds
	width, height := bounds.Dx(), bounds.Dy()
	for w, h := width/2, height/2; w >= 200 && h >= 200; w, h = w/2, h/2 {
		for y := 0; y+h <= height; y += h {
			for x := 0; x+w <= width; x += w {
				bb = append(bb, image.Rect(
					max(0, bounds.Min.X+x-w/4), max(0, bounds.Min.Y+y-h/4),
					min(bounds.Max.X, bounds.Min.X+x+w+w/4), min(bounds.Max.Y, bounds.Min.Y+y+h+h/4),
				))
			}
		}
	}
	return bb
}

func max(a, b int) int {
	if a < b {
		return b
	}
	return a
}
func min(a, b int) int {
	if a > b {
		return b
	}
	return a
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

func IsEmpty(img image.Image) bool {
	dst := image.NewGray(image.Rect(0, 0, 16, 16))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
	m := map[bool]int{true: 1, false: 0}
	var n int
	for _, pix := range dst.Pix {
		n += m[pix < 128] // ~black
	}
	fh, _ := os.Create("/tmp/x.png")
	png.Encode(fh, dst)
	fh.Close()

	//log.Println("img:", n)
	return n <= 8
}
