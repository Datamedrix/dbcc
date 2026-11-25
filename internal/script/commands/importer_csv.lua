-- importer_csv: read CSV files (A29). The delimiter is detected from the
-- first lines unless chosen; a header row names the columns; a column is
-- numeric (right-aligned, sorted as numbers) when every non-empty value of
-- the first 200 records looks like a number. Values are never changed.
local CANDIDATES = { ",", ";", "\t", "|" }
local SAMPLE = 200

-- detect picks the delimiter that occurs the same non-zero number of
-- times on most of the sampled lines, outside quoted parts (comma when
-- none occurs).
local function detect(sample, quote)
  local lines = {}
  for l in (sample .. "\n"):gmatch("([^\n]*)\n") do
    lines[#lines + 1] = l
    if #lines >= 50 then break end
  end
  -- the last sampled line may be cut by the sample size
  if #lines > 1 and sample:sub(-1) ~= "\n" then lines[#lines] = nil end
  local tally = {}
  for _, d in ipairs(CANDIDATES) do tally[d] = {} end
  local inq = false
  for _, l in ipairs(lines) do
    local c = { [","] = 0, [";"] = 0, ["\t"] = 0, ["|"] = 0 }
    for ch in l:gmatch(".") do
      if ch == quote then
        inq = not inq
      elseif not inq and c[ch] then
        c[ch] = c[ch] + 1
      end
    end
    for d, n in pairs(c) do
      if n > 0 then tally[d][n] = (tally[d][n] or 0) + 1 end
    end
  end
  local best, score = ",", 0
  for _, d in ipairs(CANDIDATES) do
    for _, lines_with in pairs(tally[d]) do
      if lines_with > score then best, score = d, lines_with end
    end
  end
  return best
end

-- looks_numeric: a number with a decimal point (1234.50, .5, 1e5).
local function looks_numeric(v)
  v = v:match("^%s*(.-)%s*$")
  return v:match("^[-+]?%d+%.?%d*$") ~= nil or v:match("^[-+]?%d*%.%d+$") ~= nil
      or v:match("^[-+]?%d+%.?%d*[eE][-+]?%d+$") ~= nil or v:match("^[-+]?%d*%.%d+[eE][-+]?%d+$") ~= nil
end

dbcc.importer{
  name       = "csv",
  title      = "CSV (comma-separated values)",
  help       = "Text tables from spreadsheets and other tools; delimiter detected or chosen, header row, quotes, encoding",
  extensions = { "csv", "tsv", "txt" },
  options = {
    { name = "delimiter", label = "Delimiter", type = "choice",
      choices = { { value = "auto", label = "auto (detect)" }, { value = ",", label = "comma  ," },
                  { value = ";", label = "semicolon  ;" }, { value = "\t", label = "tab" }, { value = "|", label = "pipe  |" } },
      default = "auto" },
    { name = "header", label = "Header row", type = "bool", default = true,
      help = "first record names the columns; off: col1, col2, …" },
    { name = "quote", label = "Quote character", type = "choice",
      choices = { { value = "\"", label = "double  \"" }, { value = "'", label = "single  '" } },
      default = "\"" },
    { name = "encoding", label = "Encoding", type = "choice",
      choices = { "utf-8", "windows-1252", "iso-8859-1" }, default = "utf-8" },
    { name = "null", label = "Text for NULL", type = "string", default = "",
      help = "an unquoted field with this text is NULL; empty: none" },
    { name = "empty_is_null", label = "Empty field is NULL", type = "bool", default = false,
      help = "an unquoted empty field is NULL" },
    { name = "numbers", label = "Detect numeric columns", type = "bool", default = true },
    { name = "decimal", label = "Decimal separator", type = "choice",
      choices = { { value = ".", label = "point (1234.50)" }, { value = ",", label = "comma (1.234,50)" } },
      default = ".",
      help = "of numeric columns: comma reads 1.234,50 as 1234.5 (the text is not changed)" },
    { name = "skip", label = "Lines to skip", type = "int", default = 0,
      help = "lines before the header (titles, comments)" },
  },
  open = function(ctx, src, opts)
    src:encoding(opts.encoding)
    src:skip_bom()
    for _ = 1, opts.skip do
      if not src:line() then break end
    end
    local delim = opts.delimiter
    if delim == "auto" then delim = detect(src:peek(65536), opts.quote) end
    local r = src:csv{ delimiter = delim, quote = opts.quote,
                       null = opts.null ~= "" and opts.null or nil, empty_is_null = opts.empty_is_null }
    local names = {}
    if opts.header then
      local h, n = r:read(true)
      if h then
        for i = 1, n do names[i] = h[i] end
      end
    end
    -- The first records are read ahead: they decide the column count
    -- without a header and which columns are numeric.
    local ahead, width = {}, #names
    for i = 1, SAMPLE do
      local row, n = r:read()
      if not row then break end
      ahead[i] = row
      if not opts.header and n > width then width = n end
    end
    local columns = {}
    for i = 1, math.max(width, 1) do
      local name = names[i]
      if name == nil or name == "" then name = "col" .. i end
      local numeric = false
      if opts.numbers then
        local seen = false
        numeric = true
        for _, row in ipairs(ahead) do
          local v = row[i]
          if v ~= nil and v ~= "" then
            seen = true
            local ok
            if opts.decimal == "," then
              -- 1200,50, -3,5, 1.234,50 and whole numbers; read like the grid
              -- reads the column (dbcc.fmt.number, strict about grouping)
              ok = dbcc.fmt.number(v, ",") ~= nil
            else
              ok = looks_numeric(v)
            end
            if not ok then numeric = false break end
          end
        end
        numeric = numeric and seen
      end
      columns[i] = { name = name, numeric = numeric, decimal = numeric and opts.decimal or nil }
    end
    local k = 0
    return {
      columns = columns,
      next = function()
        k = k + 1
        if k <= #ahead then return ahead[k] end
        return (r:read())
      end,
    }
  end,
}
