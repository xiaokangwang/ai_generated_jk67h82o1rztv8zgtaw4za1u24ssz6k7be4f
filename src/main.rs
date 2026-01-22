use crossterm::{
    event::{self, DisableMouseCapture, EnableMouseCapture, Event, KeyCode, KeyModifiers},
    execute,
    terminal::{disable_raw_mode, enable_raw_mode, EnterAlternateScreen, LeaveAlternateScreen},
};
use pinyin_tui::PinyinDatabase;
use ratatui::{
    backend::{Backend, CrosstermBackend},
    layout::{Constraint, Direction, Layout},
    style::{Color, Modifier, Style},
    text::{Line, Span},
    widgets::{Block, Borders, List, ListItem, Paragraph, Wrap},
    Frame, Terminal,
};
use std::{error::Error, io};

#[derive(Debug, PartialEq)]
enum InputMode {
    Pinyin,  // Normal pinyin input mode
    Direct,  // Direct typing mode (for mixed language)
}

struct App {
    db: PinyinDatabase,
    input: String,
    suggestions: Vec<String>,
    selected_suggestion: usize,
    output: String,
    mode: InputMode,
}

impl App {
    fn new(db: PinyinDatabase) -> Self {
        Self {
            db,
            input: String::new(),
            suggestions: Vec::new(),
            selected_suggestion: 0,
            output: String::new(),
            mode: InputMode::Pinyin,
        }
    }

    fn update_suggestions(&mut self) {
        if self.input.is_empty() {
            self.suggestions.clear();
            self.selected_suggestion = 0;
        } else {
            self.suggestions = self.db.search(&self.input);
            if self.selected_suggestion >= self.suggestions.len() {
                self.selected_suggestion = if self.suggestions.is_empty() {
                    0
                } else {
                    self.suggestions.len() - 1
                };
            }
        }
    }

    fn select_current(&mut self) {
        if !self.suggestions.is_empty() && self.selected_suggestion < self.suggestions.len() {
            let selected = self.suggestions[self.selected_suggestion].clone();
            self.output.push_str(&selected);
            self.input.clear();
            self.suggestions.clear();
            self.selected_suggestion = 0;
        }
    }

    fn select_by_number(&mut self, num: usize) {
        if num > 0 && num <= self.suggestions.len() {
            self.selected_suggestion = num - 1;
            self.select_current();
        }
    }

    fn select_next(&mut self) {
        if !self.suggestions.is_empty() {
            self.selected_suggestion = (self.selected_suggestion + 1) % self.suggestions.len();
        }
    }

    fn select_previous(&mut self) {
        if !self.suggestions.is_empty() {
            if self.selected_suggestion == 0 {
                self.selected_suggestion = self.suggestions.len() - 1;
            } else {
                self.selected_suggestion -= 1;
            }
        }
    }

    fn handle_input(&mut self, key: KeyCode, _modifiers: KeyModifiers) {
        match self.mode {
            InputMode::Pinyin => self.handle_pinyin_input(key),
            InputMode::Direct => self.handle_direct_input(key),
        }
    }

    fn handle_pinyin_input(&mut self, key: KeyCode) {
        match key {
            // Tab switches to Direct mode
            KeyCode::Tab => {
                // If there are suggestions, select current first
                if !self.suggestions.is_empty() {
                    self.select_current();
                }
                // Switch to direct typing mode
                self.mode = InputMode::Direct;
            }
            // Convert Shift+Number symbols to tone numbers
            KeyCode::Char('!') => {
                self.input.push('1');
                self.update_suggestions();
            }
            KeyCode::Char('@') => {
                self.input.push('2');
                self.update_suggestions();
            }
            KeyCode::Char('#') => {
                self.input.push('3');
                self.update_suggestions();
            }
            KeyCode::Char('$') => {
                self.input.push('4');
                self.update_suggestions();
            }
            KeyCode::Char('%') => {
                self.input.push('5');
                self.update_suggestions();
            }
            KeyCode::Char(' ') if !self.suggestions.is_empty() => {
                // Space selects current suggestion (when suggestions available)
                self.select_current();
            }
            KeyCode::Char(c) if c.is_ascii_digit() && !self.suggestions.is_empty() => {
                // Number alone = select candidate (when suggestions available)
                let num = c.to_digit(10).unwrap() as usize;
                self.select_by_number(num);
            }
            KeyCode::Char(c) => {
                // Regular character input
                self.input.push(c);
                self.update_suggestions();
            }
            KeyCode::Backspace => {
                if self.input.is_empty() {
                    self.output.pop();
                } else {
                    self.input.pop();
                    self.update_suggestions();
                }
            }
            KeyCode::Enter => {
                self.select_current();
            }
            KeyCode::Down => {
                self.select_next();
            }
            KeyCode::Up => {
                self.select_previous();
            }
            KeyCode::Left => {
                if !self.suggestions.is_empty() {
                    for _ in 0..5 {
                        self.select_previous();
                    }
                }
            }
            KeyCode::Right => {
                if !self.suggestions.is_empty() {
                    for _ in 0..5 {
                        self.select_next();
                    }
                }
            }
            _ => {}
        }
    }

    fn handle_direct_input(&mut self, key: KeyCode) {
        match key {
            // Tab switches back to Pinyin mode
            KeyCode::Tab => {
                self.mode = InputMode::Pinyin;
            }
            // In direct mode, all characters go straight to output
            KeyCode::Char(c) => {
                self.output.push(c);
            }
            KeyCode::Backspace => {
                self.output.pop();
            }
            KeyCode::Enter => {
                // Enter adds newline in direct mode
                self.output.push('\n');
            }
            KeyCode::Left | KeyCode::Right | KeyCode::Up | KeyCode::Down => {
                // Arrow keys don't do anything in direct mode
                // Could implement cursor movement in the future
            }
            _ => {}
        }
    }
}

fn main() -> Result<(), Box<dyn Error>> {
    let db_path = "data/frequency_db.json";
    println!("Loading frequency-enhanced pinyin database from {}...", db_path);
    println!("This may take a moment...");

    let db = PinyinDatabase::load_from_file(db_path)?;
    println!("Loaded {} entries (sorted by frequency)", db.entries.len());
    println!("Starting TUI Pinyin Input Tool...");

    enable_raw_mode()?;
    let mut stdout = io::stdout();
    execute!(stdout, EnterAlternateScreen, EnableMouseCapture)?;
    let backend = CrosstermBackend::new(stdout);
    let mut terminal = Terminal::new(backend)?;

    let mut app = App::new(db);
    let result = run_app(&mut terminal, &mut app);

    disable_raw_mode()?;
    execute!(
        terminal.backend_mut(),
        LeaveAlternateScreen,
        DisableMouseCapture
    )?;
    terminal.show_cursor()?;

    if let Err(err) = result {
        println!("Error: {:?}", err);
    } else {
        // Output the typed text to stdout for easy copying
        if !app.output.is_empty() {
            println!("{}", app.output);
        }
    }

    Ok(())
}

fn run_app<B: Backend>(terminal: &mut Terminal<B>, app: &mut App) -> io::Result<()> {
    loop {
        terminal.draw(|f| ui(f, app))?;

        if let Event::Key(key) = event::read()? {
            if (key.code == KeyCode::Char('c') && key.modifiers.contains(KeyModifiers::CONTROL))
                || (key.code == KeyCode::Char('q')
                    && key.modifiers.contains(KeyModifiers::CONTROL))
                || key.code == KeyCode::Esc
            {
                return Ok(());
            }

            app.handle_input(key.code, key.modifiers);
        }
    }
}

fn ui(f: &mut Frame, app: &App) {
    let chunks = Layout::default()
        .direction(Direction::Vertical)
        .margin(2)
        .constraints([
            Constraint::Length(3),
            Constraint::Length(3),
            Constraint::Min(5),
            Constraint::Length(6),
        ])
        .split(f.area());

    let title = Paragraph::new("TUI Pinyin Input Tool")
        .style(
            Style::default()
                .fg(Color::Cyan)
                .add_modifier(Modifier::BOLD),
        )
        .block(Block::default().borders(Borders::ALL));
    f.render_widget(title, chunks[0]);

    let output_text = if app.output.is_empty() {
        "[Your Chinese text will appear here]".to_string()
    } else {
        app.output.clone()
    };
    let output_title = match app.mode {
        InputMode::Pinyin => "Output",
        InputMode::Direct => "Output [DIRECT MODE - Type English/Mixed Text]",
    };
    let output_style = match app.mode {
        InputMode::Pinyin => Style::default().fg(Color::Green),
        InputMode::Direct => Style::default().fg(Color::Yellow),
    };
    let output = Paragraph::new(output_text)
        .style(output_style)
        .block(Block::default().borders(Borders::ALL).title(output_title))
        .wrap(Wrap { trim: false });
    f.render_widget(output, chunks[1]);

    // Only show suggestions in Pinyin mode
    if app.mode == InputMode::Direct {
        let direct_mode_help = Paragraph::new(
            "DIRECT MODE: All keys are typed directly to output.\n\n\
             This mode is for typing:\n\
             • English words\n\
             • Punctuation marks\n\
             • Numbers\n\
             • Mixed language content\n\n\
             Press Tab to return to Pinyin input mode."
        )
        .style(Style::default().fg(Color::Magenta))
        .block(Block::default().borders(Borders::ALL).title("Direct Typing Mode"))
        .wrap(Wrap { trim: false });
        f.render_widget(direct_mode_help, chunks[2]);
    } else {
        let suggestions_block = Block::default()
            .borders(Borders::ALL)
            .title(format!("Suggestions ({} matches)", app.suggestions.len()));

        if app.suggestions.is_empty() {
            let help_text = if app.input.is_empty() {
                "Type pinyin with or without tones:\n  • With tones: ni3hao3 → 你好 (use Shift+Number)\n  • Without tones: nihao → 你好 (fuzzy search)\n  • Abbreviated: n3h3 → 你好"
            } else {
                "No matches found. Try different pinyin..."
            };
            let no_suggestions = Paragraph::new(help_text)
                .style(Style::default().fg(Color::DarkGray))
                .block(suggestions_block)
                .wrap(Wrap { trim: false });
            f.render_widget(no_suggestions, chunks[2]);
        } else {
        // Calculate visible window (20 items) that includes the selected item
        let page_size = 20;
        let total_items = app.suggestions.len();

        // Calculate the start of the visible window
        let visible_start = if app.selected_suggestion < page_size {
            // If selected is in first page, show from start
            0
        } else if app.selected_suggestion >= total_items.saturating_sub(page_size) {
            // If selected is in last page, show the last page
            total_items.saturating_sub(page_size)
        } else {
            // Otherwise, keep selected item in the middle of the window
            app.selected_suggestion.saturating_sub(page_size / 2)
        };

        let visible_end = (visible_start + page_size).min(total_items);

        let items: Vec<ListItem> = app
            .suggestions
            .iter()
            .enumerate()
            .skip(visible_start)
            .take(visible_end - visible_start)
            .map(|(i, s)| {
                let style = if i == app.selected_suggestion {
                    Style::default()
                        .fg(Color::Black)
                        .bg(Color::Cyan)
                        .add_modifier(Modifier::BOLD)
                } else {
                    Style::default().fg(Color::White)
                };
                let number = if i < 9 { format!("{}. ", i + 1) } else { "   ".to_string() };
                let content = format!("{}{}", number, s);
                ListItem::new(content).style(style)
            })
            .collect();

            let list = List::new(items).block(suggestions_block);
            f.render_widget(list, chunks[2]);
        }
    }

    let help_lines = match app.mode {
        InputMode::Pinyin => vec![
            Line::from(vec![
                Span::styled("Mode: ", Style::default().fg(Color::Yellow)),
                Span::styled("PINYIN", Style::default().fg(Color::Cyan).add_modifier(Modifier::BOLD)),
                Span::raw(" │ Input: "),
                Span::styled(
                    &app.input,
                    Style::default()
                        .fg(Color::White)
                        .add_modifier(Modifier::BOLD),
                ),
                Span::styled(
                    "_",
                    Style::default()
                        .fg(Color::White)
                        .add_modifier(Modifier::SLOW_BLINK),
                ),
            ]),
            Line::from(""),
            Line::from(vec![
                Span::styled("Tone: ", Style::default().fg(Color::Yellow)),
                Span::styled("Shift+1-5", Style::default().fg(Color::Cyan)),
                Span::raw(" │ "),
                Span::styled("Select: ", Style::default().fg(Color::Yellow)),
                Span::styled("Space/1-9", Style::default().fg(Color::Green)),
                Span::raw(" │ "),
                Span::styled("Switch: ", Style::default().fg(Color::Yellow)),
                Span::styled("Tab→Direct", Style::default().fg(Color::Magenta)),
            ]),
            Line::from(vec![
                Span::styled("Navigate: ", Style::default().fg(Color::Yellow)),
                Span::styled("↑↓/←→", Style::default().fg(Color::Green)),
                Span::raw(" │ "),
                Span::styled("Delete: ", Style::default().fg(Color::Yellow)),
                Span::styled("Backspace", Style::default().fg(Color::Green)),
                Span::raw(" │ "),
                Span::styled("Exit: ", Style::default().fg(Color::Yellow)),
                Span::styled("Ctrl+C/Esc", Style::default().fg(Color::Red)),
            ]),
        ],
        InputMode::Direct => vec![
            Line::from(vec![
                Span::styled("Mode: ", Style::default().fg(Color::Yellow)),
                Span::styled("DIRECT", Style::default().fg(Color::Magenta).add_modifier(Modifier::BOLD)),
                Span::raw(" - Type English/punctuation directly to output"),
            ]),
            Line::from(""),
            Line::from(vec![
                Span::styled("Type: ", Style::default().fg(Color::Yellow)),
                Span::styled("All keys → Output", Style::default().fg(Color::Green)),
                Span::raw(" │ "),
                Span::styled("Enter: ", Style::default().fg(Color::Yellow)),
                Span::styled("Newline", Style::default().fg(Color::Green)),
            ]),
            Line::from(vec![
                Span::styled("Switch: ", Style::default().fg(Color::Yellow)),
                Span::styled("Tab→Pinyin", Style::default().fg(Color::Cyan)),
                Span::raw(" │ "),
                Span::styled("Delete: ", Style::default().fg(Color::Yellow)),
                Span::styled("Backspace", Style::default().fg(Color::Green)),
                Span::raw(" │ "),
                Span::styled("Exit: ", Style::default().fg(Color::Yellow)),
                Span::styled("Ctrl+C/Esc", Style::default().fg(Color::Red)),
            ]),
        ],
    };

    let input_widget = Paragraph::new(help_lines)
        .block(Block::default().borders(Borders::ALL).title("Input & Controls"))
        .wrap(Wrap { trim: false });
    f.render_widget(input_widget, chunks[3]);
}
