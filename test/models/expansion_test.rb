require "test_helper"

class ExpansionTest < ActiveSupport::TestCase
  setup do
    @user = User.create!(email: "expansion-model@example.com", password: "s3cretpass")
    @expansion = @user.expansions.create!(
      file_name: "notes.md", selected_text: "beta", occurrence: 0,
      question: "Why?", use_openai: false
    )
  end

  test "starts pending and requires all submitted fields" do
    assert_equal "pending", @expansion.status
    invalid = @user.expansions.build(file_name: "", selected_text: "", question: "")
    assert_not invalid.valid?
  end

  test "claims a pending job only once" do
    assert @expansion.claim!
    assert_equal "processing", @expansion.reload.status
    assert_not @expansion.claim!
  end

  test "records completed and failed terminal states" do
    @expansion.claim!
    @expansion.complete!("/notes--expand-1.html")
    assert_equal ["completed", "/notes--expand-1.html", nil],
      @expansion.reload.attributes.values_at("status", "url", "error_detail")

    failed = @user.expansions.create!(file_name: "notes.md", selected_text: "gamma", occurrence: 0, question: "Why?")
    failed.claim!
    failed.fail!("Generation failed.")
    assert_equal ["failed", nil, "Generation failed."],
      failed.reload.attributes.values_at("status", "url", "error_detail")
  end
end
