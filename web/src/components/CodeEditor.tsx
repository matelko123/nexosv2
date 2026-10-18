import { useEffect, useRef } from "react";
import { basicSetup } from "codemirror";
import { EditorState } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { yaml } from "@codemirror/lang-yaml";
import { HighlightStyle, syntaxHighlighting, StreamLanguage } from "@codemirror/language";
import { tags as t } from "@lezer/highlight";

const theme = EditorView.theme(
  {
    "&": { backgroundColor: "#0b0c0d", color: "#e6e1d6", fontSize: "13px", height: "100%" },
    ".cm-scroller": { fontFamily: "IBM Plex Mono, monospace", lineHeight: "1.55" },
    ".cm-content": { caretColor: "#ffb000" },
    ".cm-cursor": { borderLeftColor: "#ffb000" },
    ".cm-gutters": { backgroundColor: "#121416", color: "#6b675f", border: "none", borderRight: "1px solid #262a2e" },
    ".cm-activeLine": { backgroundColor: "rgba(255,176,0,0.05)" },
    ".cm-activeLineGutter": { backgroundColor: "rgba(255,176,0,0.08)", color: "#ffb000" },
    "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection": { backgroundColor: "rgba(255,176,0,0.25) !important" },
    ".cm-matchingBracket": { backgroundColor: "rgba(92,207,230,0.2)", outline: "none" },
    ".cm-foldPlaceholder": { backgroundColor: "#262a2e", border: "none", color: "#a49e92" },
    ".cm-tooltip": { backgroundColor: "#181b1e", border: "1px solid #343a40" },
    ".cm-panels": { backgroundColor: "#121416", color: "#e6e1d6" },
    ".cm-searchMatch": { backgroundColor: "rgba(92,207,230,0.25)" },
  },
  { dark: true },
);

const highlight = HighlightStyle.define([
  { tag: [t.propertyName, t.definition(t.propertyName)], color: "#5ccfe6" },
  { tag: [t.string, t.special(t.string)], color: "#e6e1d6" },
  { tag: [t.number, t.bool, t.null, t.atom], color: "#ffb000" },
  { tag: t.comment, color: "#6b675f", fontStyle: "italic" },
  { tag: [t.keyword, t.typeName, t.labelName], color: "#d7a6ff" },
  { tag: [t.punctuation, t.separator, t.bracket], color: "#a49e92" },
  { tag: t.meta, color: "#4ade80" },
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

  return <div ref={host} className="h-full min-h-[300px] overflow-hidden border border-rule" />;
}
