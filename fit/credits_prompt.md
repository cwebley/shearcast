# Opus credits labeling instructions

A credits-only labeling pass over the videos in `data/opus-labels/`, run on
2026-09-28 after the user asked for show housekeeping to be cut. The same day
the definition was narrowed to drop teasers for upcoming episodes (fuzzy, and
often carrying real commentary). The text below is the narrowed version. The
first pass's teaser, sign-on and sign-off segments stay in
`data/opus-credits/` marked `"excluded"`, and aren't merged. The sponsor
and selfpromo labels were already done. This pass adds only `credits`, into a
separate directory, so the agent can't see the existing labels or anyone's
guesses. Give an agent the text below with its video list filled in, then merge
with `fit/merge_credits.py`.

---

You are producing ground-truth labels for a podcast-trimming tool. Read full
YouTube caption transcripts and mark every **credits** segment with exact
timestamps. Labels must be blind. Do NOT read anything under
~/Library/Caches/shearcast/renders, any render.m4a.json, or anything under
data/opus-labels/, and don't look up SponsorBlock. Judge from the captions
alone.

Videos (channel/video_id):
<LIST>

For each video, print the full captions from the repo root
(/Users/cwebley/src/shearcast). Always use the URL form, because some IDs start
with "-":

    ./shearcast transcript "https://www.youtube.com/watch?v=<VIDEO_ID>"

The header shows the title and the video's length. Each line is a timestamp
followed by the caption text. Read every line of every transcript, not just the
end: credits can also sit mid-episode (a teaser before a break, a contact line
before a segment).

WHAT TO LABEL: **credits** is housekeeping about the show rather than its
subject:
- production credits: who produced, edited, mixed, engineered, fact-checked or
  scored it, special thanks to staff, "I'm <host>" as part of a credit roll
- network identification ("part of the Vox Media Podcast Network", "a
  production of The Verge")
- how to contact the show: email addresses, hotlines, voicemail, "drop us a
  line", "I read every email"
- an announcement of a break, "we'll be right back", with the music around it.
  Some uploads have the ads cut out, leaving only the music and the line. Start
  at the first music caption after the last spoken line and end where speech
  resumes.

NOT credits:
- a teaser for an upcoming episode or video ("all that coming up on
  Wednesday", "we'll get to that in an upcoming episode")
- a sign-on or sign-off ("From The New York Times, this is The Daily", "you've
  been watching…", "see you next week", "stay curious", "that's it for today",
  "thanks for watching")
- a thank-you to a guest, or a guest introduction
- the program's own content, including a preview of what this same episode
  will cover
- paid sponsor reads, and self-promotion such as subscribe or rate-and-review
  asks, Patreon, merch or plugs for other shows. Those are labeled separately.
  Where credits are interleaved with them, label only the credits lines.

BOUNDARIES
- **start**: the timestamp of the first caption line that belongs to the
  credits.
- **end**: the timestamp of the first caption line after the credits (content,
  a sign-off, an ad or a promo). If the credits run to the end of the video,
  use the video's length from the header.
- Convert timestamps to seconds.
- Credits lines separated by only a sign-off or a promo line are separate
  segments.

OUTPUT: for each video, write one file to `data/opus-credits/<VIDEO_ID>.json`
in exactly this shape, even when there are no credits:

    {"video_id": "...", "channel": "...", "segments": [
      {"category": "credits", "start": <s>, "end": <s>, "confidence": "high|medium|low",
       "borderline": <true|false>, "first_line": "<quoted>", "last_line": "<quoted>",
       "reason": "<one sentence>"}
    ]}

- Set borderline to true when reasonable people could disagree about whether
  it counts as credits.
- Write only those JSON files. Don't edit anything else.

When done, reply with one line per video: video_id, number of segments, and
any segment you marked low confidence or borderline.
