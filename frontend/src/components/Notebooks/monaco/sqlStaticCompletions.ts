// SQL keyword/function completions. Kernel-side completion isn't used for
// SQL cells, so this is the full completion surface for SQL.

export function registerSqlStatic(monaco: any) {
    monaco.languages.registerCompletionItemProvider('sql', {
        provideCompletionItems: (model: any, position: any) => {
            const word = model.getWordUntilPosition(position);
            const range = {
                startLineNumber: position.lineNumber,
                endLineNumber: position.lineNumber,
                startColumn: word.startColumn,
                endColumn: word.endColumn,
            };

            const suggestions = [
                { label: 'SELECT', kind: monaco.languages.CompletionItemKind.Keyword, insertText: 'SELECT ', range },
                { label: 'FROM', kind: monaco.languages.CompletionItemKind.Keyword, insertText: 'FROM ', range },
                { label: 'WHERE', kind: monaco.languages.CompletionItemKind.Keyword, insertText: 'WHERE ', range },
                { label: 'GROUP BY', kind: monaco.languages.CompletionItemKind.Keyword, insertText: 'GROUP BY ', range },
                { label: 'ORDER BY', kind: monaco.languages.CompletionItemKind.Keyword, insertText: 'ORDER BY ', range },
                { label: 'LIMIT', kind: monaco.languages.CompletionItemKind.Keyword, insertText: 'LIMIT ', range },
                { label: 'JOIN', kind: monaco.languages.CompletionItemKind.Keyword, insertText: 'JOIN ', range },
                { label: 'LEFT JOIN', kind: monaco.languages.CompletionItemKind.Keyword, insertText: 'LEFT JOIN ', range },
                { label: 'INNER JOIN', kind: monaco.languages.CompletionItemKind.Keyword, insertText: 'INNER JOIN ', range },
                { label: 'COUNT', kind: monaco.languages.CompletionItemKind.Function, insertText: 'COUNT(*)', range },
                { label: 'AVG', kind: monaco.languages.CompletionItemKind.Function, insertText: 'AVG(${1:col})', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range },
                { label: 'SUM', kind: monaco.languages.CompletionItemKind.Function, insertText: 'SUM(${1:col})', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range },
            ];
            return { suggestions };
        }
    });
}
