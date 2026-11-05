package site

// css is the one stylesheet of the site: system fonts, a reading column, light and dark from the
// browser's setting, no script and no external resource.
const css = `:root{--bg:#fff;--fg:#1c2024;--muted:#5b6672;--line:#dfe3e8;--code:#f3f5f7;--link:#0b5cad;--accent:#0b5cad}
@media (prefers-color-scheme:dark){:root{--bg:#14171a;--fg:#e4e8ec;--muted:#9aa6b2;--line:#2c3238;--code:#1e2328;--link:#6cb2f2;--accent:#6cb2f2}}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.6 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
a{color:var(--link)}
.top{display:flex;gap:1.5rem;align-items:center;flex-wrap:wrap;padding:.75rem 1rem;border-bottom:1px solid var(--line)}
.brand{font-weight:700;text-decoration:none;color:var(--fg)}
.top nav{display:flex;gap:1rem;flex-wrap:wrap}
.top nav a{text-decoration:none;color:var(--muted)}
.top nav a[aria-current]{color:var(--accent);font-weight:600}
.wrap{display:flex;gap:2rem;max-width:80rem;margin:0 auto;padding:0 1rem}
aside{flex:0 0 14rem;padding:1rem 0;font-size:.9rem}
aside ul{list-style:none;margin:0;padding:0;position:sticky;top:1rem}
aside li{margin:.15rem 0}
aside a{text-decoration:none;color:var(--muted)}
aside a[aria-current]{color:var(--accent);font-weight:600}
main{flex:1;min-width:0;max-width:52rem;padding:1rem 0 4rem}
h1,h2,h3,h4{line-height:1.25;margin:2rem 0 .75rem}
h1{margin-top:1rem}
.anchor{margin-left:.4rem;text-decoration:none;color:var(--muted);opacity:0;font-weight:400}
h1:hover .anchor,h2:hover .anchor,h3:hover .anchor,h4:hover .anchor,.anchor:focus{opacity:1}
code{background:var(--code);padding:.1em .3em;border-radius:3px;font:.88em ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
pre{background:var(--code);padding:.9rem 1rem;border-radius:6px;overflow-x:auto;line-height:1.45}
pre code{background:none;padding:0;font-size:.85rem}
table{border-collapse:collapse;display:block;overflow-x:auto;margin:1rem 0}
th,td{border:1px solid var(--line);padding:.4rem .7rem;text-align:left;vertical-align:top}
th{background:var(--code)}
blockquote{margin:1rem 0;padding:.1rem 1rem;border-left:4px solid var(--line);color:var(--muted)}
hr{border:0;border-top:1px solid var(--line)}
.unlinked{border-bottom:1px dotted var(--muted);cursor:help}
img{max-width:100%}
footer{border-top:1px solid var(--line);color:var(--muted);font-size:.85rem;padding:1rem;text-align:center}
@media (max-width:760px){.wrap{display:block}aside{padding-bottom:0}aside ul{position:static;display:flex;flex-wrap:wrap;gap:.3rem 1rem}}
`
