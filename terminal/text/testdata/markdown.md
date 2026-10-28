# Markdown Rendering Test

This document exercises the five planned block-level Markdown features.

## 1. Headings

# Level 1 heading
## Level 2 heading
### Level 3 heading
#### Level 4 heading
##### Level 5 heading
###### Level 6 heading

### Heading with **bold**, *italic*, and `code`

## 2. Lists

- First unordered item
- Second unordered item with **bold text**
  - Nested unordered item
  - Another nested item with `inline code`
- A final item long enough to demonstrate hanging indentation when it wraps across multiple terminal lines at narrower viewport widths.

1. First ordered item
2. Second ordered item
   1. Nested ordered item
   2. Another nested ordered item
3. Third ordered item

- [x] Completed task
- [ ] Incomplete task

## 3. Blockquotes

> A single-line blockquote.
>
> A multi-line blockquote with **bold text** and `inline code`.
> This line belongs to the same quote.
>
> > A nested blockquote.

## 4. Thematic breaks

Above a hyphen rule.

---

Between thematic breaks.

***

Below an underscore rule.

___

## 5. Tables

| Left aligned | Center aligned | Right aligned |
| :----------- | :------------: | ------------: |
| Plain text   | **Bold text**  |           123 |
| `code`       | *Italic text*  |         4,567 |
| A longer cell that may wrap | [Example](https://example.com) | 89 |
