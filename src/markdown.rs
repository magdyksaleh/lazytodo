use pulldown_cmark::{Event, Options, Parser, Tag};
use unicode_width::UnicodeWidthStr;

#[derive(Debug, Clone, Copy, Default)]
struct Style {
    bold: bool,
    italic: bool,
    code: bool,
}

#[derive(Debug, Clone)]
struct Segment {
    text: String,
    style: Style,
}

// Minimal inline markdown renderer that outputs ANSI-styled text and wraps to width.
// It intentionally favors simplicity over completeness for parity with the Go UI.
pub fn render_markdown_line(raw: &str, width: usize) -> String {
    if raw.trim().is_empty() {
        return String::new();
    }

    let mut stack = vec![Style::default()];
    let mut segments: Vec<Segment> = Vec::new();

    let mut options = Options::empty();
    options.insert(Options::ENABLE_STRIKETHROUGH);

    let parser = Parser::new_ext(raw, options);
    for event in parser {
        match event {
            Event::Start(tag) => {
                let mut next = *stack.last().unwrap_or(&Style::default());
                match tag {
                    Tag::Emphasis => next.italic = true,
                    Tag::Strong => next.bold = true,
                    Tag::CodeBlock(_) => next.code = true,
                    _ => {}
                }
                stack.push(next);
            }
            Event::End(_) => {
                if stack.len() > 1 {
                    stack.pop();
                }
            }
            Event::Text(text) => segments.push(Segment {
                text: text.to_string(),
                style: *stack.last().unwrap_or(&Style::default()),
            }),
            Event::Code(text) => {
                let mut style = *stack.last().unwrap_or(&Style::default());
                style.code = true;
                segments.push(Segment {
                    text: text.to_string(),
                    style,
                });
            }
            Event::SoftBreak => segments.push(Segment {
                text: " ".to_string(),
                style: *stack.last().unwrap_or(&Style::default()),
            }),
            Event::HardBreak => segments.push(Segment {
                text: "\n".to_string(),
                style: *stack.last().unwrap_or(&Style::default()),
            }),
            _ => {}
        }
    }

    let rendered = wrap_segments(&segments, width);
    rendered
        .trim_matches(|c| c == ' ' || c == '\n' || c == '\t')
        .to_string()
}

fn wrap_segments(segments: &[Segment], width: usize) -> String {
    if width == 0 {
        return segments_to_string(segments);
    }

    let mut lines: Vec<String> = Vec::new();
    let mut current = String::new();
    let mut current_width = 0usize;

    for segment in segments {
        for token in tokenize(&segment.text) {
            if token == "\n" {
                lines.push(current);
                current = String::new();
                current_width = 0;
                continue;
            }

            let token_width = UnicodeWidthStr::width(token.as_str());
            if !token.trim().is_empty() && current_width > 0 && current_width + token_width > width
            {
                lines.push(current);
                current = String::new();
                current_width = 0;
            }

            if current_width == 0 && token == " " {
                continue;
            }

            current.push_str(&apply_style(&token, segment.style));
            current_width = current_width.saturating_add(token_width);
        }
    }

    if !current.is_empty() {
        lines.push(current);
    }

    lines.join("\n")
}

fn tokenize(text: &str) -> Vec<String> {
    let mut tokens = Vec::new();
    let mut buf = String::new();
    for ch in text.chars() {
        if ch == '\n' {
            if !buf.is_empty() {
                tokens.push(buf.clone());
                buf.clear();
            }
            tokens.push("\n".to_string());
            continue;
        }
        if ch.is_whitespace() {
            if !buf.is_empty() {
                tokens.push(buf.clone());
                buf.clear();
            }
            tokens.push(" ".to_string());
            continue;
        }
        buf.push(ch);
    }
    if !buf.is_empty() {
        tokens.push(buf);
    }
    tokens
}

fn segments_to_string(segments: &[Segment]) -> String {
    let mut out = String::new();
    for seg in segments {
        out.push_str(&apply_style(&seg.text, seg.style));
    }
    out
}

fn apply_style(text: &str, style: Style) -> String {
    let mut codes = Vec::new();
    if style.bold {
        codes.push("1");
    }
    if style.italic {
        codes.push("3");
    }
    if style.code {
        codes.push("7");
    }

    if codes.is_empty() {
        return text.to_string();
    }

    format!("\x1b[{}m{}\x1b[0m", codes.join(";"), text)
}
