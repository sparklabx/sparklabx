import React from 'react';

// AWS Athena — drawn as Athena's owl (the goddess's emblem), a simple monochrome
// line mark that inherits currentColor so it tints like the other connector
// glyphs (muted, turns primary when active). Original mark, not the AWS logo.
// ponytail: simple owl glyph; swap for official artwork if branding ever matters.
export const AthenaIcon: React.FC<{ className?: string }> = ({ className }) => (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7"
        strokeLinecap="round" strokeLinejoin="round" className={className} aria-hidden="true">
        <path d="M5.5 4 8 7M18.5 4 16 7" />
        <path d="M12 21c-4.2 0-7-3.1-7-7.2C5 9.5 8 6.6 12 6.6s7 2.9 7 7.2C19 17.9 16.2 21 12 21Z" />
        <circle cx="9" cy="12.2" r="1.6" />
        <circle cx="15" cy="12.2" r="1.6" />
        <path d="M11 15.4l1 1 1-1" />
    </svg>
);
