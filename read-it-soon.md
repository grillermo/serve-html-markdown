# Read It Soon — Product Features

**Save any article to your Kindle in one click.**

---

## Core Value

Paste a URL or click a bookmarklet on any webpage — receive a clean EPUB on your e-reader within seconds.

---

## Features

**One-Click Bookmarklet**
Drag a bookmarklet to your browser bar. On any article, one click captures the page and opens a preview. No copy-pasting URLs.

**Smart Content Extraction**
4-stage pipeline ensures near-100% success:
1. Python/trafilatura direct extraction
2. Mozilla Readability (same tech as Firefox Reader View)
3. Browser-impersonating fetch fallback
4. Wayback Machine archive retrieval — even for paywalled or deleted pages

**Clean EPUB Output**
Pandoc converts extracted content to publication-quality EPUB — proper chapters, titles, author metadata, and typography. Opens natively in Kindle, Kobo, and any e-reader app.

**Email Delivery via Mailgun**
EPUB attaches to email and goes straight to your Kindle address. Subject line uses article title. No manual file transfers.

**Preview Before Sending**
Full-screen article preview before delivery — confirm content looks right, then press Send.

**Per-Reader Bookmarklets**
Landing page generates a personalized bookmarklet with recipient email embedded. Share with family or colleagues — each gets their own send-to address.

**Self-Hosted & Private**
Runs on your own server. No third-party reading queue. No tracking. Your articles stay yours.

**Zero-Dependency UX**
No browser extension to install. Works on any browser, any OS. Bookmarklet is plain JavaScript.

---

## Stack

Ruby · Node.js · Pandoc · Mailgun · Mozilla Readability
