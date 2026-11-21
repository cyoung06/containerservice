"use client";

import { useState } from "react";

import { FormField } from "@/components/form/form-field";
import { PageHeader } from "@/components/layout/page-header";
import { Button } from "@/components/ui/button";
import { ErrorText } from "@/components/ui/error-text";
import { Icon } from "@/components/ui/icon";
import { TextInput } from "@/components/ui/text-input";
import { get, put } from "@/lib/api/http";
import type { ClaudeInfo } from "@/lib/api/types";
import { formatTime } from "@/lib/formatting/format";
import { useAction } from "@/lib/hooks/use-action";
import { useResource } from "@/lib/hooks/use-resource";

const loadClaude = (): Promise<ClaudeInfo> => get<ClaudeInfo>("/api/claude");

const textareaClass =
  "w-full resize-y rounded-md border border-border bg-background px-2.5 py-2 font-mono text-xs text-foreground outline-none placeholder:text-foreground-quaternary focus:border-accent";

const Login = ({
  info,
  onDone,
}: {
  readonly info: ClaudeInfo;
  readonly onDone: () => Promise<void>;
}) => {
  const action = useAction();
  const [token, setToken] = useState("");
  const save = (value: string) =>
    void action
      .run(() => put("/api/claude/token", { token: value }))
      .then(async (done) => {
        if (done) {
          setToken("");
          await onDone();
        }
      });
  return (
    <section className="flex flex-col gap-3 rounded-lg border border-border bg-surface p-4">
      <div className="flex flex-col gap-1">
        <h2 className="text-sm font-medium text-foreground">Login</h2>
        <p className="text-xs text-foreground-tertiary">
          One long-lived token for every container with &ldquo;Claude
          login&rdquo; on, so claude starts logged in there. Make it on your
          computer with <code>claude setup-token</code> (it lasts a year).
          Containers read it as <code>CLAUDE_CODE_OAUTH_TOKEN</code>; anything
          running in them, an agent included, can read it.
        </p>
        <p className="text-xs text-foreground-secondary">
          {info.tokenSet
            ? `A token is set${info.tokenAt ? ` (${formatTime(info.tokenAt)})` : ""}.`
            : "No token is set."}
        </p>
      </div>
      <form
        className="flex flex-col gap-3 sm:flex-row sm:items-end"
        onSubmit={(event) => {
          event.preventDefault();
          if (token.trim()) {
            save(token.trim());
          }
        }}
      >
        <div className="min-w-0 flex-1">
          <FormField
            label={info.tokenSet ? "Replace the token" : "Token"}
            htmlFor="claude-token"
          >
            <TextInput
              id="claude-token"
              type="password"
              value={token}
              onChange={(event) => setToken(event.target.value)}
              placeholder="sk-ant-oat01-…"
              autoComplete="off"
              spellCheck={false}
              className="font-mono text-xs"
            />
          </FormField>
        </div>
        <div className="flex gap-2">
          <Button
            type="submit"
            variant="primary"
            disabled={!token.trim()}
            pending={action.pending}
          >
            Save
          </Button>
          {info.tokenSet ? (
            <Button
              variant="ghost"
              onClick={() => save("")}
              disabled={action.pending}
            >
              Remove
            </Button>
          ) : null}
        </div>
      </form>
      <ErrorText message={action.error} />
    </section>
  );
};

const Settings = ({
  info,
  onDone,
}: {
  readonly info: ClaudeInfo;
  readonly onDone: () => Promise<void>;
}) => {
  const action = useAction();
  const [text, setText] = useState(info.settings);
  let problem: string | null = null;
  if (text.trim() !== "") {
    try {
      const value: unknown = JSON.parse(text);
      if (typeof value !== "object" || value === null || Array.isArray(value)) {
        problem = "Settings are a JSON object.";
      }
    } catch (error) {
      problem = error instanceof Error ? error.message : "Not JSON.";
    }
  }
  const changed = text !== info.settings;
  return (
    <section className="flex flex-col gap-3 rounded-lg border border-border bg-surface p-4">
      <div className="flex flex-col gap-1">
        <h2 className="text-sm font-medium text-foreground">Shared settings</h2>
        <p className="text-xs text-foreground-tertiary">
          A settings.json for every container, as Claude Code&apos;s managed
          settings (
          <code>/etc/claude-code/managed-settings.d/console.json</code>,
          read-only there): keys set here win over a container&apos;s own
          settings; lists like <code>permissions.allow</code> add up. The claude
          image already makes skipping permissions the default. Edits reach
          running containers within a minute or so.
        </p>
      </div>
      <form
        className="flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          if (!problem && changed) {
            void action
              .run(() => put("/api/claude/settings", { settings: text }))
              .then(async (done) => {
                if (done) {
                  await onDone();
                }
              });
          }
        }}
      >
        <textarea
          value={text}
          onChange={(event) => setText(event.target.value)}
          rows={14}
          spellCheck={false}
          autoComplete="off"
          placeholder={
            '{\n  "permissions": {\n    "deny": ["Bash(git push:*)"]\n  }\n}'
          }
          className={textareaClass}
          aria-invalid={problem ? true : undefined}
        />
        <div className="flex flex-wrap items-center gap-3">
          <Button
            type="submit"
            variant="primary"
            disabled={!!problem || !changed}
            pending={action.pending}
          >
            Save
          </Button>
          {problem ? <p className="text-xs text-danger">{problem}</p> : null}
          {info.settingsAt && !changed ? (
            <p className="text-xs text-foreground-tertiary">
              Saved {formatTime(info.settingsAt)}
            </p>
          ) : null}
          <ErrorText message={action.error} />
        </div>
      </form>
    </section>
  );
};

export const ClaudePage = () => {
  const info = useResource(loadClaude);
  return (
    <>
      <PageHeader title="Claude" icon={<Icon name="terminal" size={15} />} />
      <div className="scrollbar-thin min-h-0 min-w-0 flex-1 overflow-y-auto">
        <div className="mx-auto flex max-w-[720px] flex-col gap-5 px-4 py-5 md:px-8 md:py-8">
          <ErrorText message={info.error} />
          {info.data ? (
            <>
              <Login info={info.data} onDone={info.reload} />
              <Settings
                key={info.data.settingsAt ?? "none"}
                info={info.data}
                onDone={info.reload}
              />
            </>
          ) : null}
        </div>
      </div>
    </>
  );
};
