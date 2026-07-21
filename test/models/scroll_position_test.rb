require "test_helper"

class ScrollPositionTest < ActiveSupport::TestCase
  setup do
    @user = User.create!(email: "scroll-model@example.com", password: "s3cretpass")
  end

  test "requires a file name and safe anchor" do
    position = @user.scroll_positions.build(file_name: "notes.md", anchor: "heading-1:part.two")
    assert position.valid?

    position.file_name = ""
    assert_not position.valid?

    position.file_name = "notes.md"
    ["", "</script>", "has spaces", "quote\""].each do |anchor|
      position.anchor = anchor
      assert_not position.valid?, "#{anchor.inspect} should be rejected"
    end
  end

  test "allows one position per user and file" do
    @user.scroll_positions.create!(file_name: "notes.md", anchor: "first")
    duplicate = @user.scroll_positions.build(file_name: "notes.md", anchor: "second")

    assert_not duplicate.valid?
    assert_includes duplicate.errors[:file_name], "has already been taken"
  end
end
