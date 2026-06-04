// Scala/Spark completion fallback for when the Almond kernel isn't connected
// yet. Fires alongside the kernel provider; its labels are filtered from
// kernel results to avoid duplicates.

export function registerScalaStatic(monaco: any) {
    monaco.languages.registerCompletionItemProvider('scala', {
        triggerCharacters: ['.'],
        provideCompletionItems: (model: any, position: any) => {
            const word = model.getWordUntilPosition(position);
            const range = {
                startLineNumber: position.lineNumber,
                endLineNumber: position.lineNumber,
                startColumn: word.startColumn,
                endColumn: word.endColumn,
            };

            const textUntilPosition = model.getValueInRange({
                startLineNumber: position.lineNumber,
                startColumn: 1,
                endLineNumber: position.lineNumber,
                endColumn: position.column,
            });
            const lineContent = textUntilPosition.trim();

            const K = monaco.languages.CompletionItemKind;
            const snippet = monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet;

            const rootSuggestions = [
                { label: 'spark', kind: K.Variable, insertText: 'spark', range, detail: 'SparkSession' },
                { label: 'sc', kind: K.Variable, insertText: 'sc', range, detail: 'SparkContext' },
                { label: 'df', kind: K.Variable, insertText: 'df', range, detail: 'DataFrame' },
                { label: 'val', kind: K.Keyword, insertText: 'val ${1:name} = ${2:value}', insertTextRules: snippet, range, detail: 'Immutable binding' },
                { label: 'var', kind: K.Keyword, insertText: 'var ${1:name} = ${2:value}', insertTextRules: snippet, range, detail: 'Mutable binding' },
                { label: 'def', kind: K.Keyword, insertText: 'def ${1:name}(${2:args}): ${3:Type} = ${4:body}', insertTextRules: snippet, range, detail: 'Function' },
                { label: 'println', kind: K.Function, insertText: 'println(${1:obj})', insertTextRules: snippet, range, detail: 'Print line' },
            ];

            const dfMethods = [
                { label: 'select', kind: K.Method, insertText: 'select(${1:cols})', insertTextRules: snippet, range, detail: 'Spark Select', documentation: { value: '**select(cols: Column*)**\n\nProjects a set of expressions and returns a new DataFrame.' } },
                { label: 'filter', kind: K.Method, insertText: 'filter(${1:condition})', insertTextRules: snippet, range, detail: 'Spark Filter', documentation: { value: '**filter(condition: Column)**\n\nFilters rows using the given condition.' } },
                { label: 'where', kind: K.Method, insertText: 'where(${1:condition})', insertTextRules: snippet, range, detail: 'Spark Where', documentation: { value: '**where(condition: Column)**\n\nAlias for `filter`.' } },
                { label: 'groupBy', kind: K.Method, insertText: 'groupBy(${1:cols})', insertTextRules: snippet, range, detail: 'Spark GroupBy' },
                { label: 'join', kind: K.Method, insertText: 'join(${1:other}, ${2:on}, "${3:how}")', insertTextRules: snippet, range, detail: 'Spark Join' },
                { label: 'withColumn', kind: K.Method, insertText: 'withColumn("${1:colName}", ${2:col})', insertTextRules: snippet, range, detail: 'Add or replace column' },
                { label: 'withColumnRenamed', kind: K.Method, insertText: 'withColumnRenamed("${1:old}", "${2:new}")', insertTextRules: snippet, range, detail: 'Rename column' },
                { label: 'orderBy', kind: K.Method, insertText: 'orderBy(${1:cols})', insertTextRules: snippet, range, detail: 'Spark OrderBy' },
                { label: 'show', kind: K.Method, insertText: 'show()', range, detail: 'Show DataFrame', documentation: { value: '**show(numRows: Int = 20, truncate: Boolean = true)**\n\nPrints the first `n` rows to the console.' } },
                { label: 'count', kind: K.Method, insertText: 'count()', range, detail: 'Count Rows' },
                { label: 'head', kind: K.Method, insertText: 'head()', range, detail: 'First row' },
                { label: 'describe', kind: K.Method, insertText: 'describe()', range, detail: 'Describe' },
                { label: 'printSchema', kind: K.Method, insertText: 'printSchema()', range, detail: 'Print Schema' },
                { label: 'cache', kind: K.Method, insertText: 'cache()', range, detail: 'Cache in memory' },
                { label: 'toDF', kind: K.Method, insertText: 'toDF(${1:colNames})', insertTextRules: snippet, range, detail: 'Rename columns' },
            ];

            const sparkReadMethods = [
                { label: 'csv', kind: K.Function, insertText: 'csv("${1:path}")', insertTextRules: snippet, range, detail: 'Read CSV' },
                { label: 'parquet', kind: K.Function, insertText: 'parquet("${1:path}")', insertTextRules: snippet, range, detail: 'Read Parquet' },
                { label: 'json', kind: K.Function, insertText: 'json("${1:path}")', insertTextRules: snippet, range, detail: 'Read JSON' },
                { label: 'table', kind: K.Function, insertText: 'table("${1:name}")', insertTextRules: snippet, range, detail: 'Read Table' },
                { label: 'option', kind: K.Function, insertText: 'option("${1:key}", "${2:value}")', insertTextRules: snippet, range, detail: 'Reader option' },
                { label: 'format', kind: K.Function, insertText: 'format("${1:source}")', insertTextRules: snippet, range, detail: 'Source format' },
                { label: 'load', kind: K.Function, insertText: 'load("${1:path}")', insertTextRules: snippet, range, detail: 'Load generic' },
            ];

            let suggestions: any[] = rootSuggestions;
            if (lineContent.endsWith('spark.read.')) {
                suggestions = sparkReadMethods;
            } else if (lineContent.endsWith('spark.')) {
                suggestions = [
                    { label: 'read', kind: K.Property, insertText: 'read', range, detail: 'DataFrameReader' },
                    { label: 'readStream', kind: K.Property, insertText: 'readStream', range, detail: 'DataStreamReader' },
                    { label: 'sql', kind: K.Function, insertText: 'sql("${1:query}")', insertTextRules: snippet, range, detail: 'Run SQL', documentation: { value: '**spark.sql(sqlQuery)**\n\nExecutes a SQL query returning the result as a DataFrame.' } },
                    { label: 'table', kind: K.Function, insertText: 'table("${1:name}")', insertTextRules: snippet, range, detail: 'Get table' },
                    { label: 'range', kind: K.Function, insertText: 'range(${1:start}, ${2:end})', insertTextRules: snippet, range, detail: 'Range DataFrame' },
                    { label: 'createDataFrame', kind: K.Function, insertText: 'createDataFrame(${1:data}, ${2:schema})', insertTextRules: snippet, range, detail: 'Create DataFrame' },
                    { label: 'createDataset', kind: K.Function, insertText: 'createDataset(${1:data})', insertTextRules: snippet, range, detail: 'Create Dataset' },
                    { label: 'emptyDataFrame', kind: K.Property, insertText: 'emptyDataFrame', range, detail: 'Empty DataFrame' },
                    { label: 'catalog', kind: K.Property, insertText: 'catalog', range, detail: 'Catalog API' },
                    { label: 'conf', kind: K.Property, insertText: 'conf', range, detail: 'RuntimeConfig' },
                    { label: 'sparkContext', kind: K.Property, insertText: 'sparkContext', range, detail: 'SparkContext' },
                    { label: 'streams', kind: K.Property, insertText: 'streams', range, detail: 'StreamingQueryManager' },
                    { label: 'udf', kind: K.Property, insertText: 'udf', range, detail: 'UDFRegistration' },
                    { label: 'version', kind: K.Property, insertText: 'version', range, detail: 'Spark version' },
                    { label: 'stop', kind: K.Method, insertText: 'stop()', range, detail: 'Stop session' },
                ];
            } else if (lineContent.endsWith('.')) {
                suggestions = dfMethods;
            }
            return { suggestions };
        },
    });
}
