//! Конфигурация только через env, разобранная в типизированную структуру и провалидированная
//! на старте: не хватает обязательной переменной — сервис падает сразу, а не на первом запросе.

use serde::Deserialize;

#[derive(Debug, Deserialize)]
pub struct Config {
    #[serde(default = "default_port")]
    pub port: u16,
    /// Обязательная: нет DATABASE_URL → ошибка на старте.
    pub database_url: String,
    #[serde(default = "default_log")]
    pub rust_log: String,
}

fn default_port() -> u16 {
    8080
}

fn default_log() -> String {
    "info,sqlx=warn".into()
}

impl Config {
    pub fn from_env() -> anyhow::Result<Self> {
        Ok(envy::from_env::<Config>()?)
    }
}
