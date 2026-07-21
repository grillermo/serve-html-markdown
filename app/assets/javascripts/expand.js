(function () {
  "use strict";

  let button = null;
  let popover = null;
  let currentSelection = null;

  const CSRF = () => {
    const meta = document.querySelector("meta[name='csrf-token']");
    return meta ? meta.content : "";
  };

  function topmostVisibleAnchor() {
    const anchored = document.querySelectorAll("[id]");
    let current = null;
    for (const el of anchored) {
      if (el.getBoundingClientRect().top <= 1) {
        current = el.id;
      } else {
        break;
      }
    }
    return current;
  }

  function saveScrollPosition() {
    const anchor = topmostVisibleAnchor();
    if (!anchor) return;

    const body = JSON.stringify({
      file_name: decodeURIComponent(location.pathname.slice(1)),
      anchor: anchor,
      authenticity_token: CSRF()
    });

    if (navigator.sendBeacon) {
      navigator.sendBeacon("/scroll_position", new Blob([body], { type: "application/json" }));
    } else {
      fetch("/scroll_position", {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: body,
        keepalive: true
      });
    }
  }

  document.addEventListener("DOMContentLoaded", () => {
    if (typeof window.__scrollAnchor !== "string") return;

    const target = document.getElementById(window.__scrollAnchor);
    if (target) target.scrollIntoView();
  });

  let scrollSaveTimer = null;
  window.addEventListener("scroll", () => {
    clearTimeout(scrollSaveTimer);
    scrollSaveTimer = setTimeout(saveScrollPosition, 500);
  }, { passive: true });
  window.addEventListener("pagehide", () => {
    clearTimeout(scrollSaveTimer);
    saveScrollPosition();
  });
  window.addEventListener("pagehide", () => {
    jobs.forEach((record) => clearTimeout(record.timer));
    jobs.clear();
  });

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
      top: `${window.scrollY + rect.bottom + 8}px`,
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

  const POLL_INTERVAL_MS = 1500;
  const jobs = new Map();

  function statusContainer() {
    let container = document.getElementById("expansion-statuses");
    if (container) return container;

    container = document.createElement("div");
    container.id = "expansion-statuses";
    Object.assign(container.style, {
      position: "fixed", top: "0", left: "0", width: "100%",
      boxSizing: "border-box", zIndex: "10000", display: "flex",
      flexDirection: "column", font: "14px system-ui, sans-serif"
    });
    document.body.appendChild(container);
    return container;
  }

  function addStatusBar(jobId, selection) {
    const bar = document.createElement("div");
    const content = document.createElement("span");
    const close = document.createElement("button");
    Object.assign(bar.style, {
      width: "100%", boxSizing: "border-box", minWidth: "0", padding: "10px 12px",
      background: "#1b1b1b", color: "#eee", borderBottom: "1px solid #555",
      display: "flex", alignItems: "center", gap: "12px"
    });
    Object.assign(content.style, {
      minWidth: "0", flex: "1", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap"
    });
    close.type = "button";
    close.textContent = "×";
    close.setAttribute("aria-label", "Dismiss expansion");
    Object.assign(close.style, {
      flex: "0 0 auto", border: "0", background: "transparent", color: "inherit",
      font: "24px/1 system-ui, sans-serif", cursor: "pointer", padding: "0 2px"
    });
    content.textContent = `Expanding text “${selection}”`;
    bar.append(content, close);
    statusContainer().appendChild(bar);

    const record = { bar, content, timer: null };
    jobs.set(jobId, record);
    close.addEventListener("click", () => dismissJob(jobId));
    return record;
  }

  function dismissJob(jobId) {
    const record = jobs.get(jobId);
    if (!record) return;
    clearTimeout(record.timer);
    record.bar.remove();
    jobs.delete(jobId);
    const container = document.getElementById("expansion-statuses");
    if (container && !container.children.length) container.remove();
  }

  function schedulePoll(jobId) {
    const record = jobs.get(jobId);
    if (record) record.timer = setTimeout(() => pollJob(jobId), POLL_INTERVAL_MS);
  }

  function renderCompleted(record, url) {
    const link = document.createElement("a");
    link.href = url;
    link.textContent = "Expansion ready — open it";
    link.style.color = "#bb86fc";
    record.content.replaceChildren(link);
  }

  function renderFailure(record, detail) {
    record.content.textContent = detail || "Expansion failed.";
    record.bar.style.color = "#ffaaaa";
  }

  function pollJob(jobId) {
    const record = jobs.get(jobId);
    if (!record) return;

    fetch(`/expansions/${encodeURIComponent(jobId)}`, { headers: { Accept: "application/json" } })
      .then(async (response) => {
        const data = await response.json().catch(() => ({}));
        if (!response.ok) throw new Error(data.detail || "Unable to check expansion status.");
        return data;
      })
      .then((data) => {
        const current = jobs.get(jobId);
        if (!current) return;
        if (data.status === "completed") renderCompleted(current, data.url);
        else if (data.status === "failed") renderFailure(current, data.detail);
        else schedulePoll(jobId);
      })
      .catch((error) => {
        const current = jobs.get(jobId);
        if (current) renderFailure(current, error.message);
      });
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

    textarea.addEventListener("keydown", (event) => {
      if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
        event.preventDefault();
        popover.requestSubmit();
      }
    });

    const openaiLabel = document.createElement("label");
    Object.assign(openaiLabel.style, {
      display: "flex",
      alignItems: "center",
      gap: "6px",
      font: "inherit",
      cursor: "pointer"
    });
    const openaiCheckbox = document.createElement("input");
    openaiCheckbox.type = "checkbox";
    openaiLabel.append(openaiCheckbox, document.createTextNode("Use OpenAI"));

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

    popover.append(textarea, openaiLabel, submit, message);
    popover.addEventListener("submit", (event) => {
      event.preventDefault();
      submit.disabled = true;
      submit.textContent = "Queue expansion";
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
          question: textarea.value,
          use_openai: openaiCheckbox.checked
        })
      })
        .then(async (response) => {
          const data = await response.json().catch(() => ({}));
          if (!response.ok) {
            throw new Error(data.detail || `Request failed (${response.status})`);
          }
          const jobId = data.id;
          if (!jobId) throw new Error("Expansion was not queued.");
          addStatusBar(jobId, currentSelection.text);
          removeUI();
          pollJob(jobId);
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

  function handleSelectionEnd(event) {
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
  }

  document.addEventListener("mouseup", handleSelectionEnd);
  document.addEventListener("touchend", handleSelectionEnd);

  // iOS never fires mouseup/touchend when a selection is made or adjusted by
  // dragging the native selection handles, so selectionchange is the only
  // reliable signal there. Debounce since it fires continuously mid-drag.
  let selectionChangeTimer = null;
  document.addEventListener("selectionchange", () => {
    if (popover) return;
    clearTimeout(selectionChangeTimer);
    selectionChangeTimer = setTimeout(() => {
      const selection = window.getSelection();
      const text = selection ? selection.toString().trim() : "";
      if (!text || selection.rangeCount === 0) {
        if (!popover) removeUI();
        return;
      }
      showButton(selection.getRangeAt(0), text);
    }, 300);
  });

  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") removeUI();
  });
})();
