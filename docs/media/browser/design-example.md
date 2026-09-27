# Design Mode selection

Request from the operator:

> Make the card green and the title bigger

## Element 1: `div`

- Page: http://localhost:53553/ ("Fixture shop")
- Viewport: 1280x800 CSS px, device pixel ratio 1
- Selector: `#card`
- DOM path: html › body › div#card
- Box: x=30 y=90, 180x100 (relative to the viewport)
- Text: "Blue card"
- Screenshot: element-1.png, from headless Chromium on terminal-local (a fresh render at the same URL and viewport)
- Markup: element-1.html

Computed CSS that differs from the element's defaults:

```css
background-color: rgb(0, 0, 255);
border-bottom-left-radius: 8px;
border-bottom-right-radius: 8px;
border-top-left-radius: 8px;
border-top-right-radius: 8px;
color: rgb(255, 255, 255);
font-family: sans-serif;
height: 100px;
left: 30px;
position: absolute;
top: 90px;
width: 180px;
```

Stylesheet rules that match it:

```css
#card { position: absolute; left: 30px; top: 90px; width: 180px; height: 100px; background: rgb(0, 0, 255); color: rgb(255, 255, 255); border-radius: 8px; padding: 0px; }
```

## Element 2: `h1`

- Page: http://localhost:53553/ ("Fixture shop")
- Viewport: 1280x800 CSS px, device pixel ratio 1
- Selector: `#title`
- DOM path: html › body › h1#title
- Box: x=30 y=16, 1220x32 (relative to the viewport)
- Text: "Fixture shop"
- Screenshot: element-2.png, from headless Chromium on terminal-local (a fresh render at the same URL and viewport)
- Markup: element-2.html

Computed CSS that differs from the element's defaults:

```css
font-family: sans-serif;
font-size: 28px;
height: 32px;
margin-bottom: 16px;
margin-left: 30px;
margin-right: 30px;
margin-top: 16px;
width: 1220px;
```

Stylesheet rules that match it:

```css
h1 { margin: 16px 30px; font-size: 28px; }
```

