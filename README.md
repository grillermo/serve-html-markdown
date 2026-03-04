# Serve HTML & Markdown with FastAPI

This project exposes a single endpoint that serves files from the local `/files` folder:

- `/your-file.html` -> serves HTML as-is
- `/your-file.md` -> renders Markdown to HTML
- `/your-file.markdown` -> renders Markdown to HTML

## Setup

```bash
python -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
```

## Run

```bash
uvicorn main:app --reload
```

Server default URL: `http://127.0.0.1:8000`

## Usage

1. Put files into the `files/` folder.
2. Open in browser:
   - `http://127.0.0.1:8000/example.html`
   - `http://127.0.0.1:8000/example.markdown`

## Notes

- UTF-8 text files are expected.
- Unsupported extensions return `404`.
