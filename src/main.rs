mod app;
mod edit;
mod external_edit;
mod io;
mod keys;
mod markdown;
mod model;
mod render;
mod text_input;

use std::env;
use std::fs;
use std::path::PathBuf;

use log::LevelFilter;
use simplelog::{Config, WriteLogger};

use crate::model::App;

fn main() {
    let (logging_on, path, explicit_path) = parse_args();
    if let Err(err) = init_logging(logging_on) {
        eprintln!("warning: failed to initialize logging: {}", err);
    }

    let path = match resolve_path(path, explicit_path) {
        Ok(path) => path,
        Err(err) => {
            eprintln!("{}", err);
            std::process::exit(1);
        }
    };

    let app = match App::new(path) {
        Ok(app) => app,
        Err(err) => {
            eprintln!("failed to load file: {}", err);
            std::process::exit(1);
        }
    };

    if let Err(err) = app.run() {
        eprintln!("error: {}", err);
        std::process::exit(1);
    }
}

fn parse_args() -> (bool, PathBuf, bool) {
    let mut logging_on = false;
    let mut path: Option<PathBuf> = None;

    for arg in env::args().skip(1) {
        match arg.as_str() {
            "--logs" | "-logs" => logging_on = true,
            _ => {
                if path.is_some() {
                    eprintln!("usage: lazytodo [--logs] [path]");
                    std::process::exit(1);
                }
                path = Some(PathBuf::from(arg));
            }
        }
    }

    let explicit_path = path.is_some();
    let path = path.unwrap_or_else(|| PathBuf::from("todo.md"));
    (logging_on, path, explicit_path)
}

fn resolve_path(path: PathBuf, explicit_path: bool) -> Result<PathBuf, String> {
    if explicit_path {
        if !path.exists() {
            return Err(format!("file {} does not exist", path.display()));
        }
        return Ok(fs::canonicalize(&path).unwrap_or(path));
    }

    if path.exists() {
        Ok(fs::canonicalize(&path).unwrap_or(path))
    } else {
        env::current_dir()
            .map(|cwd| cwd.join(path))
            .map_err(|e| e.to_string())
    }
}

fn init_logging(enabled: bool) -> Result<(), String> {
    if !enabled {
        log::set_max_level(LevelFilter::Off);
        return Ok(());
    }

    let file = fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open("lazytodo.log")
        .map_err(|e| e.to_string())?;

    WriteLogger::init(LevelFilter::Debug, Config::default(), file).map_err(|e| e.to_string())
}
