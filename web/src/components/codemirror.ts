import { EditorView } from '@uiw/react-codemirror'
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { tags as t } from '@lezer/highlight'

const c = (name: string, alpha?: number) => (alpha === undefined ? `rgb(var(--${name}))` : `rgb(var(--${name}) / ${alpha})`)

// cmTheme styles CodeMirror from the CSS design tokens, so it follows the
// light/dark scheme without re-creating the editor.
export const cmTheme = [
  EditorView.theme({
    '&': { backgroundColor: c('paper'), color: c('ink'), fontSize: '13px' },
    '.cm-scroller': { fontFamily: '"IBM Plex Mono", ui-monospace, monospace', lineHeight: '1.6' },
    '.cm-content': { caretColor: c('accent'), padding: '10px 0' },
    '.cm-cursor, .cm-dropCursor': { borderLeftColor: c('accent'), borderLeftWidth: '2px' },
    '&.cm-focused': { outline: 'none' },
    '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': {
      backgroundColor: c('accent', 0.18),
    },
    '.cm-gutters': { backgroundColor: c('surface'), color: c('mute'), border: 'none', borderRight: `1px solid ${c('line')}` },
    '.cm-activeLine': { backgroundColor: c('accent', 0.04) },
    '.cm-activeLineGutter': { backgroundColor: c('accent', 0.08), color: c('ink') },
    '.cm-matchingBracket, &.cm-focused .cm-matchingBracket': { backgroundColor: c('warn', 0.35), outline: 'none' },
    '.cm-tooltip': { backgroundColor: c('paper'), border: `1px solid ${c('ink')}`, color: c('ink') },
    '.cm-tooltip-autocomplete > ul > li[aria-selected]': { backgroundColor: c('accent'), color: '#fff' },
    '.cm-placeholder': { color: c('mute') },
  }),
  syntaxHighlighting(
    HighlightStyle.define([
      { tag: [t.keyword, t.controlKeyword, t.definitionKeyword, t.operatorKeyword, t.moduleKeyword], color: c('accent'), fontWeight: '500' },
      { tag: [t.string, t.special(t.string), t.regexp], color: c('danger') },
      { tag: [t.number, t.bool, t.null, t.atom], color: c('warn-text') },
      { tag: [t.comment, t.lineComment, t.blockComment], color: c('mute'), fontStyle: 'italic' },
      { tag: [t.function(t.variableName), t.function(t.propertyName), t.definition(t.variableName)], color: c('ink'), fontWeight: '600' },
      { tag: [t.propertyName, t.attributeName], color: c('body') },
      { tag: [t.operator, t.punctuation, t.bracket], color: c('sec') },
      { tag: t.invalid, color: c('danger'), textDecoration: 'underline' },
    ]),
  ),
]
