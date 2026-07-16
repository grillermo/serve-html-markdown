(function () {
  "use strict";

  let button = null;
  let popover = null;
  let currentSelection = null;

  const CSRF = () => {
    const meta = document.querySelector("meta[name='csrf-token']");
    return meta ? meta.content : "";
  };

  function removeUI() {
    if (button) { button.remove(); button = null; }
    if (popover) { popover.remove(); popover = null; }
  }

  function occurrenceIndex(range, text) {
    const pre = range.cloneRange();
    pre.selectNodeContents(document.body);
    pre.setEnd(range.startContainer, range.startOffset);
    const before = pre.toString();
    let count = 0;
    let idx = -1;
    while ((idx = before.indexOf(text, idx + 1)) !== -1) count += 1;
    return count;
  }

  function showButton(range, text) {
    removeUI();
    const rect = range.getBoundingClientRect();
    currentSelection = {
      text: text,
      occurrence: occurrenceIndex(range, text)
    };

    button = document.createElement("button");
    button.type = "button";
    button.textContent = "⤢"; // ⤢ expand icon
    button.setAttribute("aria-label", "Expand selection");
    Object.assign(button.style, {
      position: "absolute",
      left: `${window.scrollX + rect.left + rect.width / 2 - 16}px`,
      top: `${window.scrollY + rect.top - 40}px`,
      width: "32px",
      height: "32px",
      borderRadius: "6px",
      border: "1px solid #555",
      background: "#222",
      color: "#eee",
      fontSize: "18px",
      cursor: "pointer",
      zIndex: "9999"
    });
    button.addEventListener("mousedown", (event) => event.preventDefault());
    button.addEventListener("click", () => showPopover(rect));
    document.body.appendChild(button);
  }

  function showPopover(rect) {
    if (button) { button.remove(); button = null; }

    popover = document.createElement("form");
    Object.assign(popover.style, {
      position: "absolute",
      left: `${window.scrollX + rect.left}px`,
      top: `${window.scrollY + rect.bottom + 8}px`,
      width: "320px",
      padding: "12px",
      borderRadius: "8px",
      border: "1px solid #555",
      background: "#1b1b1b",
      color: "#eee",
      zIndex: "9999",
      display: "flex",
      flexDirection: "column",
      gap: "8px",
      font: "14px system-ui, sans-serif"
    });

    const textarea = document.createElement("textarea");
    textarea.placeholder = "Ask about this selection…";
    textarea.required = true;
    textarea.rows = 3;
    Object.assign(textarea.style, {
      resize: "vertical",
      background: "#111",
      color: "#eee",
      border: "1px solid #444",
      borderRadius: "4px",
      padding: "6px",
      font: "inherit"
    });

    const submit = document.createElement("button");
    submit.type = "submit";
    submit.textContent = "Expand";
    Object.assign(submit.style, {
      padding: "6px 12px",
      background: "#2d5a88",
      color: "#fff",
      border: "none",
      borderRadius: "4px",
      cursor: "pointer",
      font: "inherit"
    });

    const message = document.createElement("div");
    message.style.color = "#e08080";
    message.style.minHeight = "1em";

    popover.append(textarea, submit, message);
    popover.addEventListener("submit", (event) => {
      event.preventDefault();
      submit.disabled = true;
      submit.textContent = "Expanding… (may take a minute)";
      message.textContent = "";

      fetch("/expansions", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": CSRF()
        },
        body: JSON.stringify({
          file_name: decodeURIComponent(location.pathname.slice(1)),
          selected_text: currentSelection.text,
          occurrence: currentSelection.occurrence,
          question: textarea.value
        })
      })
        .then(async (response) => {
          const data = await response.json().catch(() => ({}));
          if (!response.ok) {
            throw new Error(data.detail || `Request failed (${response.status})`);
          }
          location.reload();
        })
        .catch((error) => {
          message.textContent = error.message;
          submit.disabled = false;
          submit.textContent = "Expand";
        });
    });

    document.body.appendChild(popover);
    textarea.focus();
  }

  document.addEventListener("mouseup", (event) => {
    if (popover && popover.contains(event.target)) return;
    if (button && button.contains(event.target)) return;

    setTimeout(() => {
      const selection = window.getSelection();
      const text = selection ? selection.toString().trim() : "";
      if (!text || selection.rangeCount === 0) {
        if (!popover) removeUI();
        return;
      }
      showButton(selection.getRangeAt(0), text);
    }, 0);
  });

  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") removeUI();
  });
})();
