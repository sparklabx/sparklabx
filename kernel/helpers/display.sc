// SparkLabX: pretty HTML rendering for Spark DataFrames in Scala notebooks.
// Use display(df) for an HTML table; df.show() stays Spark's native ASCII output.
//
// We deliberately do NOT register a jvm-repr displayer for Dataset: Ammonite
// echoes EVERY `val df = ...` binding, so a displayer would collect()+render the
// whole table on every assignment (and double up with df.show()). So unlike the
// Python twin (where a bare `df` is HTML via _repr_html_ and assignments are
// silent), Scala HTML is explicit via display(df). Python twin: 01-sparklabx-display.py.

// .toDF coerces typed Datasets to Row so .isNullAt / .get(i) are valid.
def _spxDfHtml(df: org.apache.spark.sql.Dataset[_], n: Int): String = {
    val asDf = df.toDF
    val rows = asDf.limit(n).collect()
    val cols = asDf.schema.fieldNames
    def esc(v: Any): String = Option(v).map(_.toString).getOrElse("null")
        .replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
    val thead = "<tr>" + cols.map(c => s"<th>${esc(c)}</th>").mkString + "</tr>"
    val tbody = rows.map(r => "<tr>" + cols.indices.map(i =>
        s"<td>${if (r.isNullAt(i)) "null" else esc(r.get(i))}</td>"
    ).mkString + "</tr>").mkString
    s"""<table class="dataframe">$thead$tbody</table><div style="color:#888;font-size:11px;margin-top:4px">showing first ${rows.length} rows</div>"""
}

// display(df) — explicit pretty HTML render (matches Python's display()).
def display(df: org.apache.spark.sql.Dataset[_], n: Int = 20): Unit = {
    val html = _spxDfHtml(df, n)
    try { almond.display.Html(html).display() }
    catch { case _: Throwable => println(html) } // fallback if Almond API differs
}
