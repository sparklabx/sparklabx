// Curated Python/PySpark completion snippets. Fires alongside the kernel
// provider; the kernel provider filters these labels out so they aren't
// duplicated — these win because they have placeholders + docstrings.

export function registerPythonStatic(monaco: any) {
    monaco.languages.registerCompletionItemProvider('python', {
        triggerCharacters: ['.'],
        provideCompletionItems: (model: any, position: any) => {
            const word = model.getWordUntilPosition(position);
            const range = {
                startLineNumber: position.lineNumber,
                endLineNumber: position.lineNumber,
                startColumn: word.startColumn,
                endColumn: word.endColumn,
            };

            // 1. Determine Context (Check text before cursor)
            const textUntilPosition = model.getValueInRange({
                startLineNumber: position.lineNumber,
                startColumn: 1,
                endLineNumber: position.lineNumber,
                endColumn: position.column
            });

            const lineContent = textUntilPosition.trim();

            // --- Suggestion Pools ---

            // Root Level (Global variables)
            const rootSuggestions = [
                { label: 'spark', kind: monaco.languages.CompletionItemKind.Variable, insertText: 'spark', range, detail: 'Spark Session' },
                { label: 'pd', kind: monaco.languages.CompletionItemKind.Module, insertText: 'pd', range, detail: 'Pandas' },
                { label: 'df', kind: monaco.languages.CompletionItemKind.Variable, insertText: 'df', range, detail: 'DataFrame' },
                { label: 'display', kind: monaco.languages.CompletionItemKind.Function, insertText: 'display(${1:df})', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range, detail: 'Native Display', documentation: { value: '**display(object)**\n\nRenders the object using the Data Platform native viewer.' } },
                { label: 'print', kind: monaco.languages.CompletionItemKind.Function, insertText: 'print(${1:obj})', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range, detail: 'Print' },
            ];

            // DataFrame Methods (df.*)
            const dfMethods = [
                { label: 'select', kind: monaco.languages.CompletionItemKind.Method, insertText: 'select(${1:cols})', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range, detail: 'Spark Select', documentation: { value: '**select(*cols)**\n\nProjects a set of expressions and returns a new DataFrame.' } },
                { label: 'filter', kind: monaco.languages.CompletionItemKind.Method, insertText: 'filter(${1:condition})', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range, detail: 'Spark Filter', documentation: { value: '**filter(condition)**\n\nFilters rows using the given condition.' } },
                { label: 'where', kind: monaco.languages.CompletionItemKind.Method, insertText: 'where(${1:condition})', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range, detail: 'Spark Where', documentation: { value: '**where(condition)**\n\nAlias for `filter`.' } },
                { label: 'groupBy', kind: monaco.languages.CompletionItemKind.Method, insertText: 'groupBy(${1:cols})', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range, detail: 'Spark GroupBy', documentation: { value: '**groupBy(*cols)**\n\nGroups the DataFrame using the specified columns, so we can run aggregation on them.' } },
                { label: 'join', kind: monaco.languages.CompletionItemKind.Method, insertText: 'join(${1:other}, ${2:on}, "${3:how}")', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range, detail: 'Spark Join', documentation: { value: '**join(other, on=None, how=None)**\n\nJoins with another DataFrame.' } },
                { label: 'withColumn', kind: monaco.languages.CompletionItemKind.Method, insertText: 'withColumn("${1:colName}", ${2:col})', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range, detail: 'Spark WithColumn', documentation: { value: '**withColumn(colName, col)**\n\nReturns a new DataFrame by adding a column or replacing the existing column that has the same name.' } },
                { label: 'withColumnRenamed', kind: monaco.languages.CompletionItemKind.Method, insertText: 'withColumnRenamed("${1:old}", "${2:new}")', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range, detail: 'Spark Rename', documentation: { value: '**withColumnRenamed(existing, new)**\n\nReturns a new DataFrame with a column renamed.' } },
                { label: 'orderBy', kind: monaco.languages.CompletionItemKind.Method, insertText: 'orderBy(${1:cols})', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range, detail: 'Spark OrderBy', documentation: { value: '**orderBy(*cols, **kwargs)**\n\nReturns a new DataFrame sorted by the specified column(s).' } },
                { label: 'show', kind: monaco.languages.CompletionItemKind.Method, insertText: 'show()', range, detail: 'Show DataFrame', documentation: { value: '**show(n=20, truncate=True)**\n\nPrints the first `n` rows to the console.' } },
                { label: 'count', kind: monaco.languages.CompletionItemKind.Method, insertText: 'count()', range, detail: 'Count Rows', documentation: { value: '**count()**\n\nReturns the number of rows in the DataFrame.' } },
                { label: 'head', kind: monaco.languages.CompletionItemKind.Method, insertText: 'head()', range, detail: 'Head' },
                { label: 'describe', kind: monaco.languages.CompletionItemKind.Method, insertText: 'describe()', range, detail: 'Describe' },
                { label: 'printSchema', kind: monaco.languages.CompletionItemKind.Method, insertText: 'printSchema()', range, detail: 'Print Schema' },
            ];

            // Spark Read Methods (spark.read.*)
            const sparkReadMethods = [
                { label: 'csv', kind: monaco.languages.CompletionItemKind.Function, insertText: 'csv("${1:path}", header=True)', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range, detail: 'Read CSV' },
                { label: 'parquet', kind: monaco.languages.CompletionItemKind.Function, insertText: 'parquet("${1:path}")', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range, detail: 'Read Parquet' },
                { label: 'json', kind: monaco.languages.CompletionItemKind.Function, insertText: 'json("${1:path}")', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range, detail: 'Read JSON' },
                { label: 'table', kind: monaco.languages.CompletionItemKind.Function, insertText: 'table("${1:name}")', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range, detail: 'Read Table' },
                { label: 'load', kind: monaco.languages.CompletionItemKind.Function, insertText: 'load("${1:path}")', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range, detail: 'Load Generic' },
            ];

            // Pandas Methods (pd.*)
            const pdMethods = [
                { label: 'read_csv', kind: monaco.languages.CompletionItemKind.Function, insertText: 'read_csv("${1:path}")', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range, detail: 'Read CSV' },
                { label: 'DataFrame', kind: monaco.languages.CompletionItemKind.Class, insertText: 'DataFrame(${1:data})', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range, detail: 'Pandas DataFrame' },
                { label: 'to_datetime', kind: monaco.languages.CompletionItemKind.Function, insertText: 'to_datetime(${1:arg})', insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet, range, detail: 'To Datetime' },
            ];

            let suggestions: any[] = rootSuggestions;

            // --- Logic ---
            if (lineContent.endsWith('spark.read.')) {
                suggestions = sparkReadMethods;
            } else if ((lineContent.endsWith('.') && !lineContent.endsWith('pd.') && !lineContent.endsWith('spark.read.')) || lineContent.endsWith('df.')) {
                suggestions = dfMethods;
            } else if (lineContent.endsWith('pd.')) {
                suggestions = pdMethods;
            } else if (lineContent.endsWith('spark.')) {
                const K = monaco.languages.CompletionItemKind;
                const snippet = monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet;
                suggestions = [
                    { label: 'read', kind: K.Property, insertText: 'read', range, detail: 'DataFrameReader' },
                    { label: 'readStream', kind: K.Property, insertText: 'readStream', range, detail: 'DataStreamReader' },
                    { label: 'sql', kind: K.Function, insertText: 'sql("${1:query}")', insertTextRules: snippet, range, detail: 'Run SQL', documentation: { value: '**spark.sql(sqlQuery)**\n\nExecutes a SQL query returning the result as a DataFrame.' } },
                    { label: 'table', kind: K.Function, insertText: 'table("${1:name}")', insertTextRules: snippet, range, detail: 'Get table' },
                    { label: 'range', kind: K.Function, insertText: 'range(${1:start}, ${2:end})', insertTextRules: snippet, range, detail: 'Range DataFrame' },
                    { label: 'createDataFrame', kind: K.Function, insertText: 'createDataFrame(${1:data}, ${2:schema})', insertTextRules: snippet, range, detail: 'Create DataFrame' },
                    { label: 'catalog', kind: K.Property, insertText: 'catalog', range, detail: 'Catalog API' },
                    { label: 'conf', kind: K.Property, insertText: 'conf', range, detail: 'RuntimeConfig' },
                    { label: 'sparkContext', kind: K.Property, insertText: 'sparkContext', range, detail: 'SparkContext' },
                    { label: 'streams', kind: K.Property, insertText: 'streams', range, detail: 'StreamingQueryManager' },
                    { label: 'udf', kind: K.Property, insertText: 'udf', range, detail: 'UDFRegistration' },
                    { label: 'version', kind: K.Property, insertText: 'version', range, detail: 'Spark version' },
                    { label: 'stop', kind: K.Method, insertText: 'stop()', range, detail: 'Stop session' },
                ];
            }

            return { suggestions };
        }
    });
}
