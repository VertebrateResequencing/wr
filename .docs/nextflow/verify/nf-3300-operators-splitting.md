# Operators — Splitting

**Source:** https://nextflow.io/docs/latest/reference/operator.html

## Features

### CO-splitCsv
`splitCsv([options])` — parse CSV/TSV text into records.
Options: `by:`, `charset:`, `decompress:`, `header:` (true, false, or list),
`limit:`, `quote:`, `sep:` (default: `,`), `skip:`, `strip:`.
```groovy
Channel.of('a,b\n1,2').splitCsv()                // [['a','b'],['1','2']]
Channel.of('a,b\n1,2').splitCsv(header: true)     // [[a:1, b:2]]
```

### CO-splitJson
`splitJson([options])` — parse JSON arrays/objects.
Options: `limit:`, `path:` (JSONPath-like query).
- JSON array → each element emitted separately
- JSON object → each key-value pair as `[key:K, value:V]`
```groovy
Channel.of('[1,2,3]').splitJson()
Channel.of('{"A":1}').splitJson()                 // [key:A, value:1]
```

### CO-splitText
`splitText([options]) [{ closure }]` — split multi-line text into chunks.
Options: `by:` (lines per chunk, default: 1), `charset:`, `compress:`,
`decompress:`, `elem:`, `file:`, `keepHeader:`, `limit:`.
Optional closure transforms each chunk.
```groovy
Channel.fromPath('*.txt').splitText(by: 10)
```

### CO-splitFasta
`splitFasta([options])` — split FASTA content into sequences/records.
Options: `by:`, `charset:`, `compress:`, `decompress:`, `elem:`, `file:`,
`limit:`, `record:` (map with fields: id, header, desc, text, seqString,
sequence, width), `size:`.
```groovy
Channel.fromPath('*.fa').splitFasta(record: [id: true, seqString: true])
```

### CO-splitFastq
`splitFastq([options])` — split FASTQ content.
Options: `by:`, `charset:`, `compress:`, `decompress:`, `elem:`, `file:`,
`limit:`, `pe:` (paired-end), `record:` (fields: readHeader, readString,
qualityHeader, qualityString).
```groovy
Channel.fromFilePairs('*_{1,2}.fq', flat: true).splitFastq(by: 100000, pe: true, file: true)
```

### CO-collectFile
`collectFile([options]) [{ closure }]` — collect items into file(s).
Variants:
- `collectFile(name: 'out.txt', [options])` — all to one file
- `collectFile { item -> [filename, content] }` — group by closure

Options: `cache:`, `keepHeader:`, `name:`, `newLine:`, `seed:`, `skip:`,
`sort:` (false/true/'index'/'hash'/'deep'/closure), `storeDir:`, `tempDir:`.
```groovy
Channel.of('a','b','c').collectFile(name: 'all.txt', newLine: true)
```
