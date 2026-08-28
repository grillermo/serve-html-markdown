require "test_helper"

class ScrollPositionsControllerTest < ActionDispatch::IntegrationTest
  setup do
    @user = User.create!(email: "scroll-controller@example.com", password: "s3cretpass")
    sign_in @user
  end

  test "saves and updates a position for a file" do
    patch "/scroll_position", params: { file_name: "notes.md", anchor: "first-heading" }, as: :json

    assert_response :no_content
    assert_equal "first-heading", @user.scroll_positions.find_by!(file_name: "notes.md").anchor

    post "/scroll_position", params: { file_name: "notes.md", anchor: "second-heading" }, as: :json

    assert_response :no_content
    assert_equal 1, @user.scroll_positions.where(file_name: "notes.md").count
    assert_equal "second-heading", @user.scroll_positions.find_by!(file_name: "notes.md").anchor
  end

  test "returns 400 for missing file name or anchor" do
    patch "/scroll_position", params: { anchor: "heading" }, as: :json
    assert_response :bad_request

    patch "/scroll_position", params: { file_name: "notes.md" }, as: :json
    assert_response :bad_request
  end

  test "returns 422 for an unsafe anchor" do
    patch "/scroll_position", params: { file_name: "notes.md", anchor: "</script>" }, as: :json

    assert_response :unprocessable_entity
    assert_nil @user.scroll_positions.find_by(file_name: "notes.md")
  end

  test "requires authentication" do
    sign_out @user

    patch "/scroll_position", params: { file_name: "notes.md", anchor: "heading" }, as: :json

    assert_response :unauthorized
  end
end
