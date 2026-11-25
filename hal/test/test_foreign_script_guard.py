"""A realtime reply in a script the device can't speak is dropped."""
from hal.drivers.voice._internal.realtime_turn import reply_is_foreign_script


def test_cjk_kana_hangul_dropped_on_english():
    for s in ["こんにちは", "안녕하세요", "你好世界", "元気ですか"]:
        assert reply_is_foreign_script(s, "English"), s


def test_vietnamese_dropped_on_english():
    for s in ["Đúng vậy, hôm nay trời thật dễ chịu.", "Chào bạn nhé", "Được rồi"]:
        assert reply_is_foreign_script(s, "English"), s


def test_english_kept():
    for s in ["I'm doing well, thanks!", "Sure, turning that off.", "Hello there."]:
        assert not reply_is_foreign_script(s, "English"), s


def test_matching_language_kept():
    assert not reply_is_foreign_script("Đúng vậy nhé", "Vietnamese")
    assert not reply_is_foreign_script("こんにちは", "Japanese")


if __name__ == "__main__":
    test_cjk_kana_hangul_dropped_on_english()
    test_vietnamese_dropped_on_english()
    test_english_kept()
    test_matching_language_kept()
    print("ok")


def test_chinese_reply_allowed_for_a_chinese_turn():
    # SOUL.md: a turn in Chinese is answered in Chinese characters.
    assert not reply_is_foreign_script("你好，今天怎么样？", "English", "你好")
    assert not reply_is_foreign_script("妳好，今天怎麼樣？", "English", "妳好")
    # Without a Chinese turn the guard still blocks it.
    assert reply_is_foreign_script("你好", "English", "hello")
    assert reply_is_foreign_script("你好", "English")
    # Kana or Hangul in the reply are still blocked after a Chinese turn.
    assert reply_is_foreign_script("こんにちは", "English", "你好")
    assert reply_is_foreign_script("안녕하세요", "English", "你好")
