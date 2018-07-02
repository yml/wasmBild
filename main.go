package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html/template"
	"image"
	"math"
	"strconv"
	"strings"
	"syscall/js"

	"github.com/anthonynsimon/bild/adjust"
	"github.com/anthonynsimon/bild/imgio"
	"github.com/anthonynsimon/bild/transform"
)

const (
	version    = "0.1.0-dev"
	jpegPrefix = "data:image/jpeg;base64,"
	pngPrefix  = "data:image/png;base64,"
)

func main() {
	println("starting go wasm")
	app := NewApp()
	jsa := NewJsApp(*app)
	select {
	case <-jsa.done:
		jsa.Release()
	}
	println("ending go wasm")
}

var effectTmpl = `<div><label for="{{ .Name }}">{{ .Name }}</label><input type="range" min="-{{ .Min }}" max="{{ .Max}}" value="0" step="0.1" id="{{ .Id }}"></div>`

type effectFn func(image.Image) image.Image

type Effect struct {
	Id, Name string
	Value    float64
	Min, Max int
}

func (eff *Effect) GetEffectFn() effectFn {
	switch eff.Name {
	case "brightness":
		return func(img image.Image) image.Image { return adjust.Brightness(img, eff.Value) }
	case "contrast":
		return func(img image.Image) image.Image { return adjust.Contrast(img, eff.Value) }

	default:
		log("effect not found: ", eff.Name)
		return nil
	}
}

func (eff *Effect) Render() string {
	var rendered strings.Builder
	tmpl, err := template.New(eff.Name).Parse(effectTmpl)
	if err != nil {
		log(err)
	}
	err = tmpl.Execute(&rendered, eff)
	if err != nil {
		fmt.Println(err)
	}
	return rendered.String()
}

type App struct {
	buf        bytes.Buffer
	cnt        int
	dstWidth   int
	sourceImg  image.Image
	resizedImg image.Image
	effects    []Effect
}

func NewApp() *App {
	return &App{
		effects:  make([]Effect, 0),
		cnt:      0,
		dstWidth: 200,
	}
}

func (app *App) Append(eff Effect) {
	app.effects = append(app.effects, eff)
	log("lenght of app.effects", len(app.effects))
}

func (app *App) Update(Id string, value float64) {
	for i, v := range app.effects {
		if v.Id == Id {
			v.Value = value
			app.effects[i] = v
			break
		}
	}
}

func (app *App) NewSourceImgFromString(simg string) {
	switch {
	case strings.HasPrefix(simg, jpegPrefix):
		simg = strings.TrimPrefix(simg, jpegPrefix)
	case strings.HasPrefix(simg, pngPrefix):
		simg = strings.TrimPrefix(simg, pngPrefix)
	default:
		log("unrecognized image format")
		return
	}

	reader := base64.NewDecoder(base64.StdEncoding, strings.NewReader(simg))
	var err error
	app.sourceImg, _, err = image.Decode(reader)
	if err != nil {
		log(err.Error())
		return
	}
	srcWidth, srcHeight := app.sourceImg.Bounds().Dx(), app.sourceImg.Bounds().Dy()
	dstWidth := app.dstWidth
	ratio := float64(srcHeight) / float64(srcWidth)
	dstHeight := int(math.Ceil(ratio * float64(dstWidth)))
	app.resizedImg = transform.Resize(app.sourceImg, dstWidth, dstHeight, transform.Linear)

}

func (app *App) PreviewImg() image.Image {
	img := app.resizedImg
	for _, eff := range app.effects {
		img = eff.GetEffectFn()(img)
	}
	return img
}

type JsApp struct {
	App
	done chan struct{}

	ShutdownCallback      js.Callback
	UploadCallback        js.Callback
	AddEffectCallback     js.Callback
	ChangeEffectsCallback js.Callback

	buf bytes.Buffer
}

func NewJsApp(app App) *JsApp {
	jsa := &JsApp{
		App:  app,
		done: make(chan struct{}),
	}

	jsa.ShutdownCallback = js.NewEventCallback(js.StopPropagation, func(ev js.Value) {
		log("event", ev)
		ev.Get("target").Set("disabled", true)
		getElementById("status").Set("innerText", "go wasm app is closed")
		jsa.done <- struct{}{}
	})
	getElementById("shutdownBtn").Call("addEventListener", "click", jsa.ShutdownCallback)

	jsa.UploadCallback = js.NewEventCallback(js.PreventDefault, func(ev js.Value) {
		log("event", ev)
		file := ev.Get("target").Get("files").Get("0")
		freader := js.Global().Get("FileReader").New()
		freader.Set("onload", js.NewEventCallback(js.PreventDefault, func(ev js.Value) {
			jsa.NewSourceImgFromString(ev.Get("target").Get("result").String())
			jsa.UpdateImgSrcById("previewImg", jsa.resizedImg)
			jsa.UpdateImgSrcById("targetImg", jsa.PreviewImg())
		}))
		freader.Call("readAsDataURL", file)
	})
	getElementById("uploader").Call("addEventListener", "change", jsa.UploadCallback)

	jsa.AddEffectCallback = js.NewEventCallback(js.StopPropagation, func(ev js.Value) {
		log("event", ev)
		jsa.cnt++
		effectSelector := getElementById("effectSelector")
		effectSelected := effectSelector.Get("options").Call("item", effectSelector.Get("selectedIndex"))
		log(effectSelected)
		eff := Effect{
			Id:    effectSelected.Get("id").String() + strconv.Itoa(app.cnt),
			Name:  effectSelected.Get("id").String(),
			Value: 0, // default value
			Min:   2,
			Max:   2,
		}
		jsa.Append(eff)
		getElementById("effects").Call("insertAdjacentHTML", "beforeend", eff.Render())
	})
	getElementById("addEffectBtn").Call("addEventListener", "click", jsa.AddEffectCallback)

	jsa.ChangeEffectsCallback = js.NewEventCallback(js.PreventDefault, func(ev js.Value) {
		log("event", ev)
		jsa.Update(ev.Get("target").Get("id").String(), ev.Get("target").Get("valueAsNumber").Float())
		jsa.UpdateImgSrcById("targetImg", jsa.PreviewImg())
	})
	getElementById("effects").Call("addEventListener", "change", jsa.ChangeEffectsCallback)

	return jsa
}

func (jsa *JsApp) Release() {
	// In tip callback.Close() is renamed callback.Release()
	jsa.ShutdownCallback.Close()
	jsa.UploadCallback.Close()
	jsa.AddEffectCallback.Close()
	jsa.ChangeEffectsCallback.Close()
}

func (jsa *JsApp) UpdateImgSrcById(Id string, img image.Image) {
	if img == nil {
		return
	}
	enc := imgio.JPEGEncoder(90)
	err := enc(&jsa.buf, img)
	if err != nil {
		log(err.Error())
	}
	// setting the previewImg src property
	getElementById(Id).
		Set("src", jpegPrefix+base64.StdEncoding.EncodeToString(jsa.buf.Bytes()))
	jsa.buf.Reset()
}

func getElementById(Id string) js.Value {
	return js.Global().Get("document").Call("getElementById", Id)
}

func log(args ...interface{}) {
	js.Global().Get("console").Call("log", args...)
}
