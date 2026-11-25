-- exporter_csv: the CSV export format (A28). With the default options the
-- output is the CSV that `export file.csv` has always written: a header
-- row, comma-separated, quoted only where needed, NULL as an empty field,
-- LF line ends. dbcc.fmt.csv encodes one record per call, so a large
-- export does not spend its time in Lua string code.
dbcc.exporter{
  name       = "csv",
  title      = "CSV (comma-separated values)",
  help       = "Text table for spreadsheets and other tools; delimiter, header, quoting, NULL text and line end are configurable",
  extensions = { "csv" },
  options = {
    { name = "delimiter", label = "Delimiter", type = "choice",
      choices = { { value = ",", label = "comma  ," }, { value = ";", label = "semicolon  ;" },
                  { value = "\t", label = "tab" }, { value = "|", label = "pipe  |" } },
      default = "," },
    { name = "header", label = "Header row", type = "bool", default = true,
      help = "first line with the column names" },
    { name = "quoting", label = "Quoting", type = "choice",
      choices = { { value = "minimal", label = "minimal (only where needed)" },
                  { value = "all", label = "all fields" },
                  { value = "nonnumeric", label = "nonnumeric (all but numeric columns)" },
                  { value = "none", label = "none (fails on a field that needs quotes)" } },
      default = "minimal" },
    { name = "quote", label = "Quote character", type = "choice",
      choices = { { value = "\"", label = "double  \"" }, { value = "'", label = "single  '" } },
      default = "\"" },
    { name = "null", label = "Text for NULL", type = "string", default = "",
      help = "written unquoted; empty = empty field" },
    { name = "line_ending", label = "Line end", type = "choice",
      choices = { { value = "lf", label = "LF (Unix)" }, { value = "crlf", label = "CRLF (Windows)" } },
      default = "lf" },
    { name = "bom", label = "UTF-8 byte order mark (for Excel)", type = "bool", default = false },
  },
  write = function(ctx, src, out, opts)
    local eol = opts.line_ending == "crlf" and "\r\n" or "\n"
    local n = #src.columns
    local enc = { delimiter = opts.delimiter, quote = opts.quote, quoting = opts.quoting, null = opts.null }
    -- Values arrive as text; with "nonnumeric" the columns the database
    -- reports as numeric stay unquoted (their text is kept as it is).
    if opts.quoting == "nonnumeric" then
      enc.numeric = {}
      for i, c in ipairs(src.columns) do enc.numeric[i] = c.numeric end
    end
    if opts.bom then out:write("\239\187\191") end
    if opts.header then
      local names = {}
      for i, c in ipairs(src.columns) do names[i] = c.name end
      -- Column names are never "numeric": header fields follow the plain rules.
      out:write(dbcc.fmt.csv(names, n, { delimiter = enc.delimiter, quote = enc.quote, quoting = enc.quoting }), eol)
    end
    src:each(function(row)
      out:write(dbcc.fmt.csv(row, n, enc), eol)
    end)
  end,
}
