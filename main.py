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
    return HTMLResponse(content=rendered_html)
