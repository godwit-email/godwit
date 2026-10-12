// Imgproto is a throwaway WebKit probe for the remote-image redesign (not shipped):
// does a sandboxed srcdoc iframe load images from a custom scheme under its CSP, does
// loading="lazy" work inside it, and does CSS background-image.
//
//	go run ./scripts/imgproto
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

var (
	start = time.Now()
	mu    sync.Mutex
	reqs  = map[string]int{}
)

func ts() string { return fmt.Sprintf("%5dms", time.Since(start).Milliseconds()) }

func pngBytes(n int) []byte {
	im := image.NewRGBA(image.Rect(0, 0, 60, 40))
	c := color.RGBA{uint8(n * 37), uint8(n * 91), uint8(n * 53), 255}
	for x := 0; x < 60; x++ {
		for y := 0; y < 40; y++ {
			im.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, im)
	return b.Bytes()
}

const page = `<!doctype html><html><head><meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: godwit-img:; connect-src 'self'">
<style>body{margin:0;font:12px monospace}#out{position:fixed;right:0;top:0;background:#fff;z-index:9}</style></head>
<body><div id="out"></div><div id="host"></div>
<script>
const log=(k,v)=>fetch('probe://app/log?k='+encodeURIComponent(k)+'&v='+encodeURIComponent(v)).catch(()=>{});
const FRAME_CSP="default-src 'none'; style-src 'unsafe-inline'; img-src data: godwit-img:";
function inner(tag){
  let h='<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="'+FRAME_CSP+'"><style>body{margin:0}img{display:block;width:60px;height:40px}.bg{width:60px;height:40px;background-image:url(godwit-img://m12/'+tag+'-bg)}</style></head><body>';
  h+='<div id="eager">';
  for(let i=0;i<3;i++)h+='<img src="godwit-img://m12/'+tag+'-e'+i+'">';
  h+='</div><div class="bg" id="bg"></div>';
  h+='<img id="blocked" src="https://example.com/'+tag+'.png"><img id="dataimg" src="data:image/gif;base64,R0lGODlhAQABAAAAACw=">';
  h+='<div style="height:3000px"></div><div id="lazy">';
  for(let i=0;i<40;i++)h+='<img loading="lazy" src="godwit-img://m12/'+tag+'-l'+i+'">';
  h+='</div></body></html>';return h;
}
const variants={A:'allow-same-origin',B:'allow-same-origin allow-scripts'};
const frames={};
for(const [tag,sb] of Object.entries(variants)){
  const f=document.createElement('iframe');
  f.setAttribute('sandbox',sb); f.style.cssText='width:200px;height:300px;border:1px solid #888';
  f.srcdoc=inner(tag); document.getElementById('host').appendChild(f); frames[tag]=f;
}
const report=(phase)=>{
  for(const [tag,f] of Object.entries(frames)){
    const d=f.contentDocument; if(!d){log(phase+tag,'NO contentDocument');continue}
    const imgs=[...d.images], ok=s=>imgs.filter(i=>i.src.includes(s)&&i.complete&&i.naturalWidth>0).length;
    log(phase+tag,'eager='+ok('-e')+'/3 lazy='+ok('-l')+'/40 blocked='+ok('example.com')+' data='+ok('data:')
      +' bgOK='+(d.getElementById('bg')&&getComputedStyle(d.getElementById('bg')).backgroundImage.slice(0,40)));
  }
};
setTimeout(()=>report('t1_'),2000);
// grow the frames to full content height, as the reader does, to see what lazy does then
setTimeout(()=>{for(const f of Object.values(frames)){f.style.height=f.contentDocument.documentElement.scrollHeight+'px'}},2500);
setTimeout(()=>report('t2_grown_'),4500);
// scroll the outer page: the frames sit in the page's scroller
setTimeout(()=>{window.scrollTo(0,2800);for(const f of Object.values(frames)){f.contentWindow.scrollTo(0,2800)}},5000);
setTimeout(()=>report('t3_scrolled_'),7000);
addEventListener('securitypolicyviolation',e=>log('outer-csp',e.blockedURI));
for(const f of Object.values(frames)) f.addEventListener('load',()=>{f.contentDocument.addEventListener('securitypolicyviolation',e=>log('frame-csp',e.blockedURI))});
setTimeout(()=>fetch('probe://app/done'),8000);
</script></body></html>`

func main() {
	err := mygo.Protocol.HandleFunc("probe", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/log":
			fmt.Printf("%s LOG %-14s %s\n", ts(), r.URL.Query().Get("k"), r.URL.Query().Get("v"))
		case "/done":
			mu.Lock()
			fmt.Printf("%s requests per tag/kind: %v\n", ts(), reqs)
			mu.Unlock()
			go func() { time.Sleep(200 * time.Millisecond); os.Exit(0) }()
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
		}
	})
	if err != nil {
		log.Fatal(err)
	}
	err = mygo.Protocol.HandleFunc("godwit-img", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		tag, kind, _ := strings.Cut(name, "-")
		k := tag + "/" + strings.TrimRight(kind, "0123456789")
		mu.Lock()
		reqs[k]++
		first := reqs[k] == 1
		mu.Unlock()
		if first {
			fmt.Printf("%s IMG %s referer=%q origin=%q dest=%q\n", ts(), name, r.Header.Get("Referer"), r.Header.Get("Origin"), r.Header.Get("Sec-Fetch-Dest"))
		}
		time.Sleep(150 * time.Millisecond)
		n, _ := strconv.Atoi(strings.TrimLeft(kind, "elbg"))
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes(n + 1))
	})
	if err != nil {
		log.Fatal(err)
	}
	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{Width: 520, Height: 700})
		if err := win.Page().LoadURL("probe://app/"); err != nil {
			log.Fatal(err)
		}
	})
	time.AfterFunc(30*time.Second, func() { os.Exit(3) })
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
