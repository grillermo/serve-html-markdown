from pathlib import Path

from fastapi import FastAPI, HTTPException
from fastapi.responses import HTMLResponse
from markdown_it import MarkdownIt

app = FastAPI(title="Serve HTML and Markdown")

FILES_DIR = (Path(__file__).resolve().parent / "files").resolve()
FILES_DIR.mkdir(parents=True, exist_ok=True)

ALLOWED_EXTENSIONS = {".html", ".md", ".markdown"}
md = MarkdownIt(
    "commonmark",
    {
        "html": True,
        "linkify": True,
        "typographer": True,
    },
)


DARK_THEME_CSS = """
body {
    background-color: #121212;
    color: #e0e0e0;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
    line-height: 1.6;
    margin: 40px auto;
    max-width: 800px;
    padding: 0 20px;
}
h1, h2, h3, h4, h5, h6 {
    color: #ffffff;
    margin-top: 1.5em;
    margin-bottom: 0.5em;
}
a {
    color: #bb86fc;
    text-decoration: none;
}
a:hover {
    text-decoration: underline;
}
code {
    background: #1e1e1e;
    padding: 0.2em 0.4em;
    border-radius: 4px;
    font-family: Consolas, Monaco, 'Andale Mono', 'Ubuntu Mono', monospace;
    font-size: 0.9em;
}
pre {
    background: #1e1e1e;
    padding: 1em;
    border-radius: 8px;
    overflow-x: auto;
}
pre code {
    background: none;
    padding: 0;
}
blockquote {
    border-left: 4px solid #333;
    margin: 0;
    padding-left: 1em;
    color: #888;
}
table {
    border-collapse: collapse;
    width: 100%;
    margin-bottom: 1em;
}
th, td {
    border: 1px solid #333;
    padding: 8px;
    text-align: left;
}
th {
    background-color: #1e1e1e;
}
img {
    max-width: 100%;
}
"""

HTML_TEMPLATE = """
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{title}</title>
    <style>
        {css}
    </style>
</head>
<body>
    {content}
</body>
</html>
"""

def resolve_file_path(file_name: str) -> Path:
    file_path = (FILES_DIR / file_name).resolve()

    try:
        file_path.relative_to(FILES_DIR)
    except ValueError as exc:
        raise HTTPException(status_code=400, detail="Invalid file path.") from exc

    if file_path.suffix.lower() not in ALLOWED_EXTENSIONS:
        raise HTTPException(
            status_code=404,
            detail="Only .html, .md, and .markdown files are supported.",
        )

    if not file_path.is_file():
        raise HTTPException(status_code=404, detail=f"File not found: {file_name}")

    return file_path


@app.get("/{file_name}", response_class=HTMLResponse)
def serve_file(file_name: str) -> HTMLResponse:
    file_path = resolve_file_path(file_name)
    content = file_path.read_text(encoding="utf-8")

    if file_path.suffix.lower() == ".html":
        return HTMLResponse(content=content)

    rendered_html = md.render(content)
    full_html = HTML_TEMPLATE.format(
        title=file_path.name,
        css=DARK_THEME_CSS,
        content=rendered_html
    )
    return HTMLResponse(content=full_html)
