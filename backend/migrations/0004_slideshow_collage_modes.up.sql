ALTER TABLE slideshow_settings
    ADD COLUMN collage_interval INT NOT NULL DEFAULT 8 AFTER collage_show_prices,
    ADD COLUMN collage_modes VARCHAR(100) NOT NULL DEFAULT 'scroll,reveal,filmstrip' AFTER collage_interval;
