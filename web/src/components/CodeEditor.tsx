import { useEffect, useRef } from "react";
import { basicSetup } from "codemirror";
import { EditorState } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { yaml } from "@codemirror/lang-yaml";
import { HighlightStyle, syntaxHighlighting, StreamLanguage } from "@codemirror/language";
import { tags as t } from "@lezer/highlight";

// Colors come from the app tokens, so the editor follows light and dark appearance.
const theme = EditorView.theme({
  "&": { backgroundColor: "var(--color-panel)", color: "var(--color-ink)", fontSize: "13px", height: "100%" },
  ".cm-scroller": { fontFamily: "var(--font-mono)", lineHeight: "1.6" },
  ".cm-content": { caretColor: "var(--color-accent)" },
  ".cm-cursor": { borderLeftColor: "var(--color-accent)", borderLeftWidth: "2px" },
  ".cm-gutters": { backgroundColor: "var(--color-panel)", color: "var(--color-ink-3)", border: "none", borderRight: "1px solid var(--color-rule)" },
  ".cm-activeLine": { backgroundColor: "color-mix(in srgb, var(--color-accent) 6%, transparent)" },
  ".cm-activeLineGutter": { backgroundColor: "transparent", color: "var(--color-ink)" },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection": {
    backgroundColor: "color-mix(in srgb, var(--color-accent) 28%, transparent) !important",
  },
  ".cm-matchingBracket": { backgroundColor: "color-mix(in srgb, var(--color-cyan) 22%, transparent)", outline: "none" },
  ".cm-foldPlaceholder": { backgroundColor: "var(--color-panel-2)", border: "none", color: "var(--color-ink-2)" },
  ".cm-tooltip": { backgroundColor: "var(--color-panel)", border: "none", borderRadius: "8px", boxShadow: "var(--sheet-shadow)" },
  ".cm-panels": { backgroundColor: "var(--color-panel-2)", color: "var(--color-ink)" },
  ".cm-searchMatch": { backgroundColor: "color-mix(in srgb, var(--color-warn) 30%, transparent)" },
});

const highlight = HighlightStyle.define([
  { tag: [t.propertyName, t.definition(t.propertyName)], color: "var(--color-accent)" },
  { tag: [t.string, t.special(t.string)], color: "var(--color-ink)" },
  { tag: [t.number, t.bool, t.null, t.atom], color: "var(--color-violet)" },
  { tag: t.comment, color: "var(--color-ink-3)", fontStyle: "italic" },
  { tag: [t.keyword, t.typeName, t.labelName], color: "var(--color-alarm)" },
  { tag: [t.punctuation, t.separator, t.bracket], color: "var(--color-ink-3)" },
  { tag: t.meta, color: "var(--color-signal)" },
]);

// .env files: KEY=value with comments.
const dotenv = StreamLanguage.define({
  token(stream) {
    if (stream.sol() && stream.match(/^\s*#.*/)) return "comment";
    if (stream.sol() && stream.match(/^\s*[A-Za-z_][A-Za-z0-9_]*(?==)/)) return "propertyName";
    if (stream.match("=")) return "punctuation";
    stream.skipToEnd();
    return "string";
  },
});

/**
 * Controlled CodeMirror editor. `value` is applied when it differs from the
 * editor content (e.g. after loading), `onChange` receives user edits.
 */
export function CodeEditor({
  value,
  onChange,
  language = "yaml",
  readOnly = false,
  onSave,
}: {
  value: string;
  onChange?: (v: string) => void;
  language?: "yaml" | "env";
  readOnly?: boolean;
  onSave?: () => void;
}) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const change = useRef(onChange);
  change.current = onChange;
  const save = useRef(onSave);
  save.current = onSave;

  useEffect(() => {
    if (!host.current) return;
    const v = new EditorView({
      parent: host.current,
      state: EditorState.create({
        doc: value,
        extensions: [
          basicSetup,
          language === "yaml" ? yaml() : dotenv,
          theme,
          syntaxHighlighting(highlight),
          EditorState.tabSize.of(2),
          EditorView.editable.of(!readOnly),
          EditorState.readOnly.of(readOnly),
          keymap.of([
            {
              key: "Mod-s",
              preventDefault: true,
              run: () => {
                save.current?.();
                return true;
              },
            },
          ]),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) change.current?.(u.state.doc.toString());
          }),
        ],
      }),
    });
    view.current = v;
    return () => v.destroy();
    // The editor is created once per language/readOnly; value syncs below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [language, readOnly]);

  useEffect(() => {
    const v = view.current;
    if (v && v.state.doc.toString() !== value) {
      v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } });
    }
  }, [value]);

  return <div ref={host} className="h-full min-h-[300px] overflow-hidden rounded-lg border border-rule" />;
}
