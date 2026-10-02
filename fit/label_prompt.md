# Opus labeling instructions

These are the instructions used to produce `data/opus-labels/`: 44 videos
labeled on 2026-09-27 by Opus subagents working from captions alone, each
agent given a batch of about four hours of transcript. On the 46 ad blocks that
SponsorBlock also marks, the labels agree within 5s at both edges on 42, with a
median gap of 0.5s. To label more videos, give an agent the text below with
its video list filled in, then score renders with `fit/score_labels.py`.

Labels must stay blind. The agent must not see shearcast's cuts or
SponsorBlock, or the labels stop being an independent check.

---

You are producing ground-truth labels for an ad-skipping tool. Read full
YouTube caption transcripts and mark every promotional segment with exact
timestamps. Labels must be blind. Do NOT read anything under
~/Library/Caches/shearcast/renders or any render.m4a.json, and don't look up
SponsorBlock. Judge from the captions alone.

Videos (channel/video_id):
<LIST>

For each video, print the full captions. Always use the URL form, because some
IDs start with "-":

    ./shearcast transcript "https://www.youtube.com/watch?v=<VIDEO_ID>"

The header shows the title and the video's length. Each line is a timestamp
(M:SS or H:MM:SS) followed by the caption text. Read every line of every
transcript, not just the parts that look promotional.

WHAT TO LABEL
- **sponsor**: a paid promotion. This includes a host-read sponsor spot and a
  dynamically inserted ad (often starting with ">>", "Support for the show
  comes from…", or a [music] gap).
- **selfpromo**: unpaid promotion of the creator's own things: merch, Patreon,
  memberships, courses, apps or games they made, newsletters, their other
  shows or channels, network cross-plugs. Also asking viewers to like,
  subscribe, comment or hit the bell, and "link in the description" pitches.
- **credits**: housekeeping about the show rather than its subject: who
  produced, edited, mixed or scored it, network identification ("part of the
  Vox Media Podcast Network"), how to contact the show (email, hotline), and
  an announcement of a break ("we'll be right back") with the music around it.
  Not credits: a teaser for an upcoming episode ("all that coming up on
  Wednesday"), which can carry real commentary; a sign-on or sign-off ("From
  The New York Times, this is The Daily", "see you next week"); a thank-you to
  a guest.
- Do NOT label real content that merely mentions a product, guest
  introductions, or editorial segments such as a news roundup that sells
  nothing.

BOUNDARIES
- **start**: the timestamp of the first caption line that belongs to the
  promotion. Include a segue written to lead into it (for example "speaking of
  staying healthy…" when it pivots into the pitch). Exclude the tail of a real
  answer or explanation that happens to come right before it.
- **end**: the timestamp of the first caption line where real content resumes.
  If the promotion runs to the end of the video, use the video's length from
  the header.
- Convert timestamps to seconds. Caption lines are only accurate to the
  second, which is fine.
- Adjacent promotions with no content between them may be one segment. Keep a
  paid sponsor and an unrelated selfpromo as separate segments.

OUTPUT: for each video, write one file to `data/opus-labels/<VIDEO_ID>.json`
in exactly this shape:

    {"video_id": "...", "channel": "...", "title": "...", "duration": <seconds>, "segments": [
      {"category": "sponsor|selfpromo|credits", "start": <s>, "end": <s>, "confidence": "high|medium|low",
       "borderline": <true|false>, "first_line": "<quoted>", "last_line": "<quoted>",
       "advertiser": "<name or null>", "reason": "<one sentence>"}
    ]}

- Use `"segments": []` if a video has no promotion.
- Set borderline to true when reasonable people could disagree about whether
  it counts as promotion at all. Examples: a creator's game discussed at
  length, a cross-plug for a sibling show, a guest's own product plugged by
  the guest.
- Write only those JSON files. Don't edit anything else.

When done, reply with one line per video: video_id, number of segments, and
any segment you marked low confidence or borderline.
