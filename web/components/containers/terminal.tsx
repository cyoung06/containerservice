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
    void Promise.all([import("@xterm/xterm"), import("@xterm/addon-fit")]).then(([{ Terminal: XTerm }, { FitAddon }]) => {
      if (disposed) {
        return;
      }
      const styles = getComputedStyle(document.documentElement);
      const terminal = new XTerm({
        cursorBlink: true,
        fontFamily: '"Geist Mono Variable", ui-monospace, monospace',
        fontSize: 13,
        theme: {
          background: styles.getPropertyValue("--background-secondary").trim() || undefined,
          foreground: styles.getPropertyValue("--foreground").trim() || undefined,
        },
      });
      const fit = new FitAddon();
      terminal.loadAddon(fit);
      terminal.open(element);
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
