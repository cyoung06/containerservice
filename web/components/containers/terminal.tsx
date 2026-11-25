"use client";

import { useEffect, useRef, useState, type ReactNode } from "react";

import "@xterm/xterm/css/xterm.css";

import { containerUrl } from "@/lib/api/containers";
import { socketUrl } from "@/lib/api/http";

import { ConsoleFrame, type ConsoleStatus } from "./console-frame";

/**
 * A shell in the container: the pod's exec subresource, passed through by containerservice. The
 * frames are Kubernetes' channel protocol: a channel byte, then data. 0 is stdin, 1 stdout, 2
 * stderr, 3 the exit status (JSON), 4 terminal size (JSON); v5 adds 255, "close channel".
 */
const stdin = 0;
const resizeChannel = 4;

/** A dark terminal whatever the page's theme, with muted, readable ANSI colors. */
/** Opens a link from the terminal in a new tab: http(s) only (no javascript: and the like). */
const openLink = (uri: string) => {
  try {
    const url = new URL(uri);
    if (url.protocol === "http:" || url.protocol === "https:") {
      window.open(url.href, "_blank", "noopener,noreferrer");
    }
  } catch {
    // not a URL
  }
};

/**
 * The clipboard, fed by OSC 52 ("c;<base64>"). Browsers that let a page write the clipboard only
 * during a user's action (Safari) get a write started at that action (expect) with a promise the
 * text fills in when it arrives; others are written to directly.
 */
class Clipboard {
  private pending: { resolve: (blob: Blob) => void; reject: () => void; timer: number } | null = null;

  /** A copy should come soon (the user just selected in tmux): start the write now. */
  expect() {
    if (typeof ClipboardItem === "undefined" || !navigator.clipboard?.write) {
      return;
    }
    this.drop();
    let resolve: (blob: Blob) => void = () => {};
    let reject: () => void = () => {};
    const text = new Promise<Blob>((ok, fail) => {
      resolve = ok;
      reject = fail;
    });
    text.catch(() => {}); // nothing came: the write just doesn't happen
    const timer = window.setTimeout(() => {
      reject();
      this.pending = null;
    }, 1500);
    this.pending = { resolve, reject, timer };
    void navigator.clipboard.write([new ClipboardItem({ "text/plain": text })]).catch(() => {});
  }

  /** An OSC 52 sequence's data: "<selection>;<base64>" ("?" asks for the clipboard: not answered). */
  receive(data: string) {
    const payload = data.slice(data.indexOf(";") + 1);
    if (payload === "?" || payload === "") {
      return;
    }
    let text: string;
    try {
      text = new TextDecoder().decode(Uint8Array.from(atob(payload), (c) => c.charCodeAt(0)));
    } catch {
      return;
    }
    if (this.pending) {
      window.clearTimeout(this.pending.timer);
      this.pending.resolve(new Blob([text], { type: "text/plain" }));
      this.pending = null;
      return;
    }
    void navigator.clipboard?.writeText(text).catch(() => {});
  }

  private drop() {
    if (this.pending) {
      window.clearTimeout(this.pending.timer);
      this.pending.reject();
      this.pending = null;
    }
  }
}

const terminalTheme = {
  background: "#16181d",
  foreground: "#d8dbe2",
  cursor: "#d8dbe2",
  cursorAccent: "#16181d",
  selectionBackground: "#3a4150",
  black: "#1f2229",
  red: "#e06c6c",
  green: "#8fbf74",
  yellow: "#d9b860",
  blue: "#6aa2e8",
  magenta: "#c08ad8",
  cyan: "#5fbfbf",
  white: "#c7cbd4",
  brightBlack: "#5c6370",
  brightRed: "#f08585",
  brightGreen: "#a8d48e",
  brightYellow: "#ecd07d",
  brightBlue: "#8ab8f5",
  brightMagenta: "#d4a5e8",
  brightCyan: "#7fd6d6",
  brightWhite: "#f2f4f8",
};

export const Terminal = ({
  name,
  fill = false,
  actions,
}: {
  readonly name: string;
  readonly fill?: boolean;
  readonly actions?: ReactNode;
}) => {
  const containerRef = useRef<HTMLDivElement>(null);
  const [status, setStatus] = useState<ConsoleStatus>("connecting");
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const element = containerRef.current;
    if (!element) {
      return;
    }
    let disposed = false;
    let cleanup = () => {};
    void Promise.all([import("@xterm/xterm"), import("@xterm/addon-fit"), import("@xterm/addon-web-links")]).then(([{ Terminal: XTerm }, { FitAddon }, { WebLinksAddon }]) => {
      if (disposed) {
        return;
      }
      const terminal = new XTerm({
        cursorBlink: true,
        // tmux has the mouse (scrolling, its own copy, which reaches the clipboard over OSC 52 in
        // browsers that allow it: not Safari). Option-drag selects in the page instead, for ⌘C
        // anywhere (Shift-drag does on other systems).
        macOptionClickForcesSelection: true,
        fontFamily: '"Geist Mono Variable", ui-monospace, monospace',
        fontSize: 13,
        // Truecolor programs (Claude Code's orange, editors' themes) draw their own colors; this is
        // the dark palette for everything that uses the 16 ANSI colors.
        theme: terminalTheme,
        // OSC 8 hyperlinks (Claude Code marks its links so): opened in a new tab, http(s) only.
        linkHandler: { activate: (_event, uri) => openLink(uri) },
      });
      const fit = new FitAddon();
      terminal.loadAddon(fit);
      // Plain URLs in the output, clickable too.
      terminal.loadAddon(new WebLinksAddon((_event, uri) => openLink(uri)));
      terminal.open(element);
      const clipboard = new Clipboard();
      // OSC 52: tmux (its mouse selection) and editors put text on the clipboard this way.
      const osc52 = terminal.parser.registerOscHandler(52, (data) => {
        clipboard.receive(data);
        return true;
      });
      // A drag hands tmux the selection, and its copy comes back a moment later: start the
      // clipboard write now, while the mouse-up still counts as the user's doing (Safari insists).
      let downAt: { x: number; y: number } | null = null;
      const onDown = (event: MouseEvent) => {
        downAt = { x: event.clientX, y: event.clientY };
      };
      const onUp = (event: MouseEvent) => {
        const dragged = downAt !== null && Math.hypot(event.clientX - downAt.x, event.clientY - downAt.y) > 3;
        downAt = null;
        if ((dragged || event.detail >= 2) && !terminal.hasSelection()) {
          clipboard.expect();
        }
      };
      element.addEventListener("mousedown", onDown, true);
      element.addEventListener("mouseup", onUp, true);
      // ⌘C (Ctrl+Shift+C elsewhere) copies the page's own selection (⌥-drag), within the key press.
      terminal.attachCustomKeyEventHandler((event) => {
        const copyKey = event.type === "keydown" && event.key.toLowerCase() === "c" && (event.metaKey || (event.ctrlKey && event.shiftKey));
        if (copyKey && terminal.hasSelection()) {
          void navigator.clipboard?.writeText(terminal.getSelection()).catch(() => {});
          event.preventDefault();
          return false;
        }
        return true;
      });
      fit.fit();
      terminal.focus();

      const socket = new WebSocket(socketUrl(containerUrl(name, "terminal")), ["v5.channel.k8s.io", "v4.channel.k8s.io"]);
      socket.binaryType = "arraybuffer";
      const encoder = new TextEncoder();
      const decoder = new TextDecoder();
      const frame = (channel: number, data: Uint8Array) => {
        if (socket.readyState === WebSocket.OPEN) {
          const out = new Uint8Array(data.length + 1);
          out[0] = channel;
          out.set(data, 1);
          socket.send(out);
        }
      };
      const sendSize = () => frame(resizeChannel, encoder.encode(JSON.stringify({ Width: terminal.cols, Height: terminal.rows })));
      socket.onopen = () => {
        setStatus("connected");
        sendSize();
      };
      socket.onmessage = (event: MessageEvent<ArrayBuffer | string>) => {
        if (typeof event.data === "string") {
          return; // The binary protocols only.
        }
        const data = new Uint8Array(event.data);
        if (data.length === 0) {
          return;
        }
        const channel = data[0];
        if (channel === 1 || channel === 2) {
          terminal.write(data.subarray(1));
        } else if (channel === 3) {
          // The exit status: {"status":"Success"} or a Failure with a message.
          try {
            const status = JSON.parse(decoder.decode(data.subarray(1))) as { status?: string; message?: string };
            if (status.status !== "Success" && status.message) {
              terminal.write(`\r\n\x1b[31m${status.message}\x1b[0m\r\n`);
            }
          } catch {
            // Not JSON: nothing to show.
          }
        }
      };
      socket.onclose = () => {
        if (!disposed) {
          setStatus("closed");
          terminal.write("\r\n\x1b[2m[terminal closed]\x1b[0m\r\n");
        }
      };
      const typing = terminal.onData((text) => frame(stdin, encoder.encode(text)));
      const binary = terminal.onBinary((text) => frame(stdin, Uint8Array.from(text, (character) => character.charCodeAt(0) & 0xff)));
      const resized = terminal.onResize(sendSize);
      const observer = new ResizeObserver(() => fit.fit());
      observer.observe(element);

      cleanup = () => {
        osc52.dispose();
        element.removeEventListener("mousedown", onDown, true);
        element.removeEventListener("mouseup", onUp, true);
        observer.disconnect();
        typing.dispose();
        binary.dispose();
        resized.dispose();
        socket.onclose = null;
        socket.close();
        terminal.dispose();
      };
    });
    return () => {
      disposed = true;
      cleanup();
    };
  }, [name, attempt]);

  return (
    <ConsoleFrame
      fill={fill}
      status={status}
      actions={actions}
      onReconnect={() => {
        setStatus("connecting");
        setAttempt((value) => value + 1);
      }}
    >
      <div key={attempt} ref={containerRef} className="h-full w-full p-2" />
    </ConsoleFrame>
  );
};
