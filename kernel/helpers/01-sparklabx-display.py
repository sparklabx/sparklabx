"""SparkLabX DataFrame display helpers — auto-loaded into every Python kernel.

Makes a bare `df` (or display(df)) render as a pretty HTML table, while
df.show() keeps Spark's native ASCII output — consistent with the Scala kernel,
where .show() can't be overridden. Built straight from collect() so the image
doesn't need pandas (toPandas would). Scala twin: display.sc.
"""
from html import escape as _spx_esc
from pyspark.sql import DataFrame as _SparkDF
from IPython.display import display as _spx_display, HTML as _SpxHTML


def _spx_df_to_html(self, n=20, truncate=False):
    cols = self.schema.names
    rows = self.limit(n).collect()
    # truncate semantics: False/0/True → no cap (HTML scrolls horizontally);
    # int N → trim each cell to N chars + "...". Defaults to "show everything"
    # since the narrow-terminal motivation behind Spark's default doesn't apply.
    limit = int(truncate) if isinstance(truncate, int) and not isinstance(truncate, bool) else 0

    def _cell(v):
        if v is None:
            return "null"
        s = str(v)
        if limit > 0 and len(s) > limit:
            s = s[:max(1, limit - 3)] + "..."
        return _spx_esc(s)

    thead = "<tr>" + "".join(f"<th>{_spx_esc(c)}</th>" for c in cols) + "</tr>"
    tbody = "".join(
        "<tr>" + "".join(f"<td>{_cell(r[i])}</td>" for i in range(len(cols))) + "</tr>"
        for r in rows
    )
    note = f'<div style="color:#888;font-size:11px;margin-top:4px">showing first {len(rows)} rows</div>'
    return f'<table class="dataframe">{thead}{tbody}</table>{note}'


# Bare `df` (a cell's last expression) → HTML table (Jupyter hook). We do NOT
# override _SparkDF.show — .show() stays Spark's native ASCII, matching Scala.
_SparkDF._repr_html_ = lambda self: _spx_df_to_html(self, 50, False)


def display(_obj, n=20, truncate=False):
    """Databricks-style: display(df, 5) → 5-row HTML table; non-DataFrame args
    fall through to IPython's display so existing code isn't broken."""
    if isinstance(_obj, _SparkDF):
        _spx_display(_SpxHTML(_spx_df_to_html(_obj, n, truncate)))
    else:
        _spx_display(_obj)
