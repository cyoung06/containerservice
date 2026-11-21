"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

import { ApiError, get, signedOutEvent } from "@/lib/api/http";
import { loadContainers, loadVolumes } from "@/lib/api/containers";
import type { Container, Volume } from "@/lib/api/types";
import { messageOf } from "@/lib/errors";

/**
 * Containers and volumes, live: one EventSource on /api/events for the whole app. The server sends
 * "changed" once when the stream starts and again whenever a container or volume changes; each one
 * reads both lists again. There is no polling. EventSource reconnects by itself after a dropped
 * connection; when the server refuses (it closes the stream when the session expires), the session
 * is asked once: a 401 signs the user out, anything else leaves a notice with a Reconnect button.
 */
type ContainersValue = {
  /** null until the first read. */
  readonly containers: readonly Container[] | null;
  readonly volumes: readonly Volume[] | null;
  /** Why the last read failed, if it did. */
  readonly error: string | null;
  /** Live updates stopped and why; null while they run. */
  readonly stopped: string | null;
  readonly reconnect: () => void;
  /** Reads both lists now (after an action, so its answer shows without waiting for the event). */
  readonly refresh: () => Promise<void>;
};

const ContainersContext = createContext<ContainersValue | null>(null);

export const ContainersProvider = ({ children }: { readonly children: ReactNode }) => {
  const [containers, setContainers] = useState<readonly Container[] | null>(null);
  const [volumes, setVolumes] = useState<readonly Volume[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [stopped, setStopped] = useState<string | null>(null);
  const [generation, setGeneration] = useState(0);

  const refresh = useCallback(async () => {
    try {
      const [nextContainers, nextVolumes] = await Promise.all([loadContainers(), loadVolumes()]);
      setContainers(nextContainers);
      setVolumes(nextVolumes);
      setError(null);
    } catch (failure) {
      setError(messageOf(failure));
    }
  }, []);

  useEffect(() => {
    const source = new EventSource("/api/events");
    let closed = false;
    source.addEventListener("changed", () => {
      setStopped(null);
      if (!closed) {
        void refresh();
      }
    });
    source.onerror = () => {
      if (source.readyState !== EventSource.CLOSED || closed) {
        return; // Reconnecting by itself.
      }
      void get("/api/session").then(
        () => setStopped("Live updates stopped: the server refused the event stream."),
        (failure: unknown) => {
          if (!(failure instanceof ApiError && failure.status === 401)) {
            setStopped(`Live updates stopped: ${messageOf(failure)}`);
          }
        },
      );
    };
    const close = () => {
      closed = true;
      source.close();
    };
    window.addEventListener(signedOutEvent, close);
    return () => {
      window.removeEventListener(signedOutEvent, close);
      close();
    };
  }, [generation, refresh]);

  const reconnect = useCallback(() => {
    setStopped(null);
    setGeneration((value) => value + 1);
  }, []);

  const value = useMemo(
    (): ContainersValue => ({ containers, volumes, error, stopped, reconnect, refresh }),
    [containers, volumes, error, stopped, reconnect, refresh],
  );
  return <ContainersContext.Provider value={value}>{children}</ContainersContext.Provider>;
};

export const useContainers = (): ContainersValue => {
  const value = useContext(ContainersContext);
  if (!value) {
    throw new Error("useContainers must be used inside ContainersProvider.");
  }
  return value;
};
