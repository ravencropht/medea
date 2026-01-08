-- Создание таблицы рабочих процессов
CREATE TABLE IF NOT EXISTS workflows (
    id SERIAL PRIMARY KEY,
    workflowname VARCHAR(255) NOT NULL,
    workflowtemplate VARCHAR(255) NOT NULL,
    namespace VARCHAR(255) NOT NULL,
    cluster VARCHAR(255) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
